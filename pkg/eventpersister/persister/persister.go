// Copyright 2022 The Bucketeer Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package persister

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/golang/protobuf/proto" // nolint:staticcheck
	"github.com/golang/protobuf/ptypes"
	"go.uber.org/zap"

	"github.com/bucketeer-io/bucketeer/pkg/errgroup"
	"github.com/bucketeer-io/bucketeer/pkg/eventpersister/datastore"
	v2ec "github.com/bucketeer-io/bucketeer/pkg/eventpersister/storage/v2"
	featureclient "github.com/bucketeer-io/bucketeer/pkg/feature/client"
	featurestorage "github.com/bucketeer-io/bucketeer/pkg/feature/storage"
	"github.com/bucketeer-io/bucketeer/pkg/health"
	"github.com/bucketeer-io/bucketeer/pkg/log"
	"github.com/bucketeer-io/bucketeer/pkg/metrics"
	"github.com/bucketeer-io/bucketeer/pkg/pubsub/puller"
	"github.com/bucketeer-io/bucketeer/pkg/pubsub/puller/codes"
	bigtable "github.com/bucketeer-io/bucketeer/pkg/storage/v2/bigtable"
	"github.com/bucketeer-io/bucketeer/pkg/storage/v2/postgres"
	eventproto "github.com/bucketeer-io/bucketeer/proto/event/client"
	esproto "github.com/bucketeer-io/bucketeer/proto/event/service"
	featureproto "github.com/bucketeer-io/bucketeer/proto/feature"
)

var (
	ErrUnexpectedMessageType = errors.New("eventpersister: unexpected message type")
)

type eventMap map[string]proto.Message
type environmentEventMap map[string]eventMap

type options struct {
	maxMPS        int
	numWorkers    int
	flushSize     int
	flushInterval time.Duration
	flushTimeout  time.Duration
	metrics       metrics.Registerer
	logger        *zap.Logger
}

type Option func(*options)

func WithMaxMPS(mps int) Option {
	return func(opts *options) {
		opts.maxMPS = mps
	}
}

func WithNumWorkers(n int) Option {
	return func(opts *options) {
		opts.numWorkers = n
	}
}

func WithFlushSize(s int) Option {
	return func(opts *options) {
		opts.flushSize = s
	}
}

func WithFlushInterval(i time.Duration) Option {
	return func(opts *options) {
		opts.flushInterval = i
	}
}

func WithFlushTimeout(timeout time.Duration) Option {
	return func(opts *options) {
		opts.flushTimeout = timeout
	}
}

func WithMetrics(r metrics.Registerer) Option {
	return func(opts *options) {
		opts.metrics = r
	}
}

func WithLogger(l *zap.Logger) Option {
	return func(opts *options) {
		opts.logger = l
	}
}

type Persister struct {
	featureClient         featureclient.Client
	puller                puller.RateLimitedPuller
	datastore             datastore.Writer
	userEvaluationStorage featurestorage.UserEvaluationsStorage
	group                 errgroup.Group
	opts                  *options
	logger                *zap.Logger
	ctx                   context.Context
	cancel                func()
	doneCh                chan struct{}
	postgresClient        postgres.Client
}

func NewPersister(
	featureClient featureclient.Client,
	p puller.Puller,
	ds datastore.Writer,
	bt bigtable.Client,
	postgresClient postgres.Client,
	opts ...Option,
) *Persister {
	dopts := &options{
		maxMPS:        1000,
		numWorkers:    1,
		flushSize:     50,
		flushInterval: 5 * time.Second,
		flushTimeout:  20 * time.Second,
		logger:        zap.NewNop(),
	}
	for _, opt := range opts {
		opt(dopts)
	}
	if dopts.metrics != nil {
		registerMetrics(dopts.metrics)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Persister{
		featureClient:         featureClient,
		puller:                puller.NewRateLimitedPuller(p, dopts.maxMPS),
		datastore:             ds,
		userEvaluationStorage: featurestorage.NewUserEvaluationsStorage(bt),
		opts:                  dopts,
		logger:                dopts.logger.Named("persister"),
		ctx:                   ctx,
		cancel:                cancel,
		doneCh:                make(chan struct{}),
		postgresClient:        postgresClient,
	}
}

func (p *Persister) Run() error {
	defer close(p.doneCh)
	p.group.Go(func() error {
		return p.puller.Run(p.ctx)
	})
	for i := 0; i < p.opts.numWorkers; i++ {
		p.group.Go(p.batch)
	}
	return p.group.Wait()
}

func (p *Persister) Stop() {
	p.cancel()
	<-p.doneCh
}

func (p *Persister) Check(ctx context.Context) health.Status {
	select {
	case <-p.ctx.Done():
		p.logger.Error("Unhealthy due to context Done is closed", zap.Error(p.ctx.Err()))
		return health.Unhealthy
	default:
		if p.group.FinishedCount() > 0 {
			p.logger.Error("Unhealthy", zap.Int32("FinishedCount", p.group.FinishedCount()))
			return health.Unhealthy
		}
		return health.Healthy
	}
}

func (p *Persister) batch() error {
	batch := make(map[string]*puller.Message)
	timer := time.NewTimer(p.opts.flushInterval)
	defer timer.Stop()
	for {
		select {
		case msg, ok := <-p.puller.MessageCh():
			if !ok {
				return nil
			}
			receivedCounter.Inc()
			id := msg.Attributes["id"]
			if id == "" {
				msg.Ack()
				// TODO: better log format for msg data
				handledCounter.WithLabelValues(codes.MissingID.String()).Inc()
				continue
			}
			if previous, ok := batch[id]; ok {
				previous.Ack()
				p.logger.Warn("Message with duplicate id", zap.String("id", id))
				handledCounter.WithLabelValues(codes.DuplicateID.String()).Inc()
			}
			batch[id] = msg
			if len(batch) < p.opts.flushSize {
				continue
			}
			p.send(batch)
			batch = make(map[string]*puller.Message)
			timer.Reset(p.opts.flushInterval)
		case <-timer.C:
			if len(batch) > 0 {
				p.send(batch)
				batch = make(map[string]*puller.Message)
			}
			timer.Reset(p.opts.flushInterval)
		case <-p.ctx.Done():
			return nil
		}
	}
}

func (p *Persister) send(messages map[string]*puller.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.flushTimeout)
	defer cancel()
	envEvents := p.extractEvents(messages)
	if len(envEvents) == 0 {
		p.logger.Error("all messages were bad")
		return
	}
	fails := make(map[string]bool, len(messages))
	for environmentNamespace, events := range envEvents {
		evs := make(map[string]string, len(events))
		for id, event := range events {
			if p.postgresClient != nil {
				if err := p.createEvent(event, id, environmentNamespace); err != nil {
					p.logger.Error(
						"failed to store an event",
						zap.Error(err),
						zap.String("id", id),
						zap.String("environmentNamespace", environmentNamespace),
					)
				}
			}
			eventJSON, repeatable, err := p.marshalEvent(event, environmentNamespace)
			if err != nil {
				if !repeatable {
					p.logger.Error(
						"failed to marshal an unrepeatable event",
						zap.Error(err),
						zap.String("id", id),
						zap.String("environmentNamespace", environmentNamespace),
					)
				}
				fails[id] = repeatable
				continue
			}
			evs[id] = eventJSON
		}
		if len(evs) > 0 {
			fs, err := p.datastore.Write(ctx, evs, environmentNamespace)
			if err != nil {
				p.logger.Error(
					"could not write to datastore",
					zap.Error(err),
					zap.String("environmentNamespace", environmentNamespace),
				)
			}
			for id, f := range fs {
				fails[id] = f
			}
		}
	}
	for id, m := range messages {
		if repeatable, ok := fails[id]; ok {
			if repeatable {
				m.Nack()
				handledCounter.WithLabelValues(codes.RepeatableError.String()).Inc()
			} else {
				m.Ack()
				handledCounter.WithLabelValues(codes.NonRepeatableError.String()).Inc()
			}
			continue
		}
		m.Ack()
		handledCounter.WithLabelValues(codes.OK.String()).Inc()
	}
}

func (p *Persister) extractEvents(messages map[string]*puller.Message) environmentEventMap {
	envEvents := environmentEventMap{}
	handleBadMessage := func(m *puller.Message, err error) {
		m.Ack()
		p.logger.Error("bad message", zap.Error(err), zap.Any("msg", m))
		handledCounter.WithLabelValues(codes.BadMessage.String()).Inc()
	}
	for _, m := range messages {
		event := &eventproto.Event{}
		if err := proto.Unmarshal(m.Data, event); err != nil {
			handleBadMessage(m, err)
			continue
		}
		var innerEvent ptypes.DynamicAny
		if err := ptypes.UnmarshalAny(event.Event, &innerEvent); err != nil {
			handleBadMessage(m, err)
			continue
		}
		if innerEvents, ok := envEvents[event.EnvironmentNamespace]; ok {
			innerEvents[event.Id] = innerEvent.Message
			continue
		}
		envEvents[event.EnvironmentNamespace] = eventMap{event.Id: innerEvent.Message}
	}
	return envEvents
}

func (p *Persister) marshalEvent(event interface{}, environmentNamespace string) (string, bool, error) {
	switch event := event.(type) {
	case *eventproto.EvaluationEvent:
		return p.marshalEvaluationEvent(event, environmentNamespace)
	case *eventproto.GoalEvent:
		return p.marshalGoalEvent(event, environmentNamespace)
	case *esproto.UserEvent:
		return p.marshalUserEvent(event, environmentNamespace)
	}
	return "", false, ErrUnexpectedMessageType
}

func (p *Persister) marshalEvaluationEvent(
	e *eventproto.EvaluationEvent,
	environmentNamespace string,
) (string, bool, error) {
	m := map[string]string{}
	m["environmentNamespace"] = environmentNamespace
	m["sourceId"] = e.SourceId.String()
	m["tag"] = e.Tag
	m["timestamp"] = time.Unix(e.Timestamp, 0).Format(time.RFC3339)
	m["featureId"] = e.FeatureId
	m["featureVersion"] = strconv.FormatInt(int64(e.FeatureVersion), 10)
	m["userId"] = e.UserId
	m["metric.userId"] = e.UserId
	m["variationId"] = e.VariationId
	if e.Reason != nil {
		m["reason"] = e.Reason.Type.String()
	}
	if e.User != nil {
		for k, v := range e.User.Data {
			m[userMetadataColumn(environmentNamespace, k)] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", false, err
	}
	return string(b), false, nil
}

func (p *Persister) marshalGoalEvent(e *eventproto.GoalEvent, environmentNamespace string) (string, bool, error) {
	m := map[string]interface{}{}
	m["environmentNamespace"] = environmentNamespace
	m["sourceId"] = e.SourceId.String()
	m["tag"] = e.Tag
	m["timestamp"] = time.Unix(e.Timestamp, 0).Format(time.RFC3339)
	m["goalId"] = e.GoalId
	m["userId"] = e.UserId
	m["metric.userId"] = e.UserId
	if e.User != nil {
		for k, v := range e.User.Data {
			m[userMetadataColumn(environmentNamespace, k)] = v
		}
	}
	m["value"] = strconv.FormatFloat(e.Value, 'f', -1, 64)
	ue, retriable, err := p.getEvaluations(e, environmentNamespace)
	if err != nil {
		return "", retriable, err
	}
	evaluations := []string{}
	for _, eval := range ue {
		reason := ""
		if eval.Reason != nil {
			reason = eval.Reason.Type.String()
		}
		evaluations = append(
			evaluations,
			fmt.Sprintf("%s:%d:%s:%s", eval.FeatureId, eval.FeatureVersion, eval.VariationId, reason),
		)
	}
	if len(evaluations) == 0 {
		p.logger.Warn(
			"Goal event has no evaluations",
			zap.String("environmentNamespace", environmentNamespace),
			zap.String("sourceId", e.SourceId.String()),
			zap.String("goalId", e.GoalId),
			zap.String("userId", e.UserId),
			zap.String("tag", e.Tag),
			zap.String("timestamp", time.Unix(e.Timestamp, 0).Format(time.RFC3339)),
		)
	}
	m["evaluations"] = evaluations
	b, err := json.Marshal(m)
	if err != nil {
		return "", false, err
	}
	return string(b), false, nil
}

func (p *Persister) getEvaluations(
	e *eventproto.GoalEvent,
	environmentNamespace string,
) ([]*featureproto.Evaluation, bool, error) {
	// Evaluations field in the GoalEvent is deprecated.
	// The following conditions should be removed once all client SDKs are updated.
	if e.SourceId == eventproto.SourceId_GOAL_BATCH {
		// Because the Goal Batch Transformer includes events from the new and old SDKs
		// we need to check both cases.
		// If both cases fail, it will save the event with no evaluations
		var ue []*featureproto.Evaluation
		ue, err := p.getCurrentUserEvaluations(environmentNamespace, e.UserId, e.Tag)
		if err != nil {
			if err == bigtable.ErrKeyNotFound {
				// Old SDK
				resp, err := p.featureClient.EvaluateFeatures(p.ctx, &featureproto.EvaluateFeaturesRequest{
					User:                 e.User,
					EnvironmentNamespace: environmentNamespace,
					Tag:                  e.Tag,
				})
				if err != nil {
					return nil, false, err
				}
				return resp.UserEvaluations.Evaluations, false, nil
			}
			// Retry
			return nil, true, err
		}
		return ue, false, nil
	}
	// Old SDK implementation doesn't include the Tag, so we use the evaluations from the client
	if e.Tag == "" {
		return e.Evaluations, false, nil
	}
	// New SDK implementation
	ue, err := p.getCurrentUserEvaluations(environmentNamespace, e.UserId, e.Tag)
	if err != nil && err != bigtable.ErrKeyNotFound {
		p.logger.Error(
			"Failed to get user evaluations",
			zap.Error(err),
			zap.String("environmentNamespace", environmentNamespace),
			zap.String("sourceId", e.SourceId.String()),
			zap.String("goalId", e.GoalId),
			zap.String("userId", e.UserId),
			zap.String("tag", e.Tag),
			zap.String("timestamp", time.Unix(e.Timestamp, 0).Format(time.RFC3339)),
		)
		return nil, true, err
	}
	return ue, false, nil
}

func (p *Persister) getCurrentUserEvaluations(
	environmentNamespace,
	userID,
	tag string,
) ([]*featureproto.Evaluation, error) {
	evaluations, err := p.userEvaluationStorage.GetUserEvaluations(
		p.ctx,
		userID,
		environmentNamespace,
		tag,
	)
	if err != nil {
		return nil, err
	}
	return evaluations, nil
}

func (p *Persister) marshalUserEvent(e *esproto.UserEvent, environmentNamespace string) (string, bool, error) {
	m := map[string]interface{}{}
	m["environmentNamespace"] = environmentNamespace
	m["sourceId"] = e.SourceId.String()
	m["tag"] = e.Tag
	m["timestamp"] = time.Unix(e.LastSeen, 0).Format(time.RFC3339)
	m["userId"] = e.UserId
	b, err := json.Marshal(m)
	if err != nil {
		return "", false, err
	}
	return string(b), false, nil
}

func userMetadataColumn(environmentNamespace string, key string) string {
	if environmentNamespace == "" {
		return fmt.Sprintf("user.data.%s", key)
	}
	return fmt.Sprintf("%s.user.data.%s", environmentNamespace, key)
}

func (p *Persister) createEvent(event interface{}, id, environmentNamespace string) error {
	switch event := event.(type) {
	case *eventproto.EvaluationEvent:
		return p.createEvaluationEvent(event, id, environmentNamespace)
	case *eventproto.GoalEvent:
		return p.createGoalEvent(event, id, environmentNamespace)
	case *esproto.UserEvent:
		return p.createUserEvent(event, id, environmentNamespace)
	}
	return ErrUnexpectedMessageType
}

func (p *Persister) createEvaluationEvent(
	event *eventproto.EvaluationEvent,
	id, environmentNamespace string,
) error {
	eventStorage := v2ec.NewEventCreationStorage(p.postgresClient)
	if err := eventStorage.CreateEvaluationEvent(p.ctx, event, id, environmentNamespace); err != nil {
		p.logger.Error(
			"Failed to store evaluation event",
			log.FieldsFromImcomingContext(p.ctx).AddFields(
				zap.Error(err),
				zap.String("environmentNamespace", environmentNamespace),
			)...,
		)
		return err
	}
	return nil
}

func (p *Persister) createGoalEvent(
	event *eventproto.GoalEvent,
	id, environmentNamespace string,
) error {
	ue, _, err := p.getEvaluations(event, environmentNamespace)
	if err != nil {
		return err
	}
	evaluations := []string{}
	for _, eval := range ue {
		reason := ""
		if eval.Reason != nil {
			reason = eval.Reason.Type.String()
		}
		evaluations = append(
			evaluations,
			fmt.Sprintf("%s:%d:%s:%s", eval.FeatureId, eval.FeatureVersion, eval.VariationId, reason),
		)
	}
	if len(evaluations) == 0 {
		p.logger.Warn(
			"Goal event has no evaluations",
			zap.String("environmentNamespace", environmentNamespace),
			zap.String("sourceId", event.SourceId.String()),
			zap.String("goalId", event.GoalId),
			zap.String("userId", event.UserId),
			zap.String("tag", event.Tag),
			zap.String("timestamp", time.Unix(event.Timestamp, 0).Format(time.RFC3339)),
		)
	}
	eventStorage := v2ec.NewEventCreationStorage(p.postgresClient)
	if err := eventStorage.CreateGoalEvent(p.ctx, event, id, environmentNamespace, evaluations); err != nil {
		p.logger.Error(
			"Failed to store goal event",
			log.FieldsFromImcomingContext(p.ctx).AddFields(
				zap.Error(err),
				zap.String("environmentNamespace", environmentNamespace),
			)...,
		)
		return err
	}
	return nil
}

func (p *Persister) createUserEvent(
	event *esproto.UserEvent,
	id, environmentNamespace string,
) error {
	eventStorage := v2ec.NewEventCreationStorage(p.postgresClient)
	if err := eventStorage.CreateUserEvent(p.ctx, event, id, environmentNamespace); err != nil {
		p.logger.Error(
			"Failed to store user event",
			log.FieldsFromImcomingContext(p.ctx).AddFields(
				zap.Error(err),
				zap.String("environmentNamespace", environmentNamespace),
			)...,
		)
		return err
	}
	return nil
}
