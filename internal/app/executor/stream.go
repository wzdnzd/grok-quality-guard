package executor

import (
	"context"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const modelStreamCloseGrace = time.Second

type streamReadResult struct {
	Response pluginapi.HostModelStreamReadResponse
	Err      error
}

type modelStreamReadFunc func(context.Context, string) (pluginapi.HostModelStreamReadResponse, error)

type modelStreamReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	events chan streamReadResult
	done   chan struct{}
}

func (s *Service) openModelStreamReader(ctx context.Context, streamID string) *modelStreamReader {
	return newModelStreamReader(ctx, streamID, s.env.ReadModelStream)
}

func newModelStreamReader(ctx context.Context, streamID string, read modelStreamReadFunc) *modelStreamReader {
	if ctx == nil {
		ctx = context.Background()
	}
	readerCtx, cancel := context.WithCancel(ctx)
	reader := &modelStreamReader{
		ctx:    readerCtx,
		cancel: cancel,
		events: make(chan streamReadResult, 1),
		done:   make(chan struct{}),
	}
	go func() {
		defer close(reader.done)
		defer close(reader.events)
		for {
			response, errRead := read(readerCtx, streamID)
			result := streamReadResult{Response: response, Err: errRead}
			select {
			case reader.events <- result:
			case <-readerCtx.Done():
				return
			}
			if errRead != nil || response.Done {
				return
			}
		}
	}()
	return reader
}

func (reader *modelStreamReader) Next() (pluginapi.HostModelStreamReadResponse, error) {
	if reader == nil {
		return pluginapi.HostModelStreamReadResponse{}, context.Canceled
	}
	select {
	case <-reader.ctx.Done():
		return pluginapi.HostModelStreamReadResponse{}, reader.ctx.Err()
	case result, ok := <-reader.events:
		if !ok {
			return pluginapi.HostModelStreamReadResponse{}, context.Canceled
		}
		return result.Response, result.Err
	}
}

func (reader *modelStreamReader) Close() {
	if reader == nil {
		return
	}
	reader.cancel()
	timer := time.NewTimer(modelStreamCloseGrace)
	defer timer.Stop()
	select {
	case <-reader.done:
	case <-timer.C:
	}
}

func (s *Service) readModelStreamContext(ctx context.Context, streamID string) (pluginapi.HostModelStreamReadResponse, error) {
	if ctx == nil || ctx.Done() == nil {
		return s.env.ReadModelStream(context.Background(), streamID)
	}
	resultChannel := make(chan streamReadResult, 1)
	go func() {
		response, errRead := s.env.ReadModelStream(ctx, streamID)
		resultChannel <- streamReadResult{Response: response, Err: errRead}
	}()
	select {
	case <-ctx.Done():
		return pluginapi.HostModelStreamReadResponse{}, ctx.Err()
	case result := <-resultChannel:
		return result.Response, result.Err
	}
}
