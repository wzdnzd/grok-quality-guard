package probe

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type streamReadResult struct {
	Response pluginapi.HostModelStreamReadResponse
	Err      error
}

type modelStreamReader struct {
	ctx    context.Context
	cancel context.CancelFunc
	events chan streamReadResult
	done   chan struct{}
}

func (s *Service) openModelStreamReader(ctx context.Context, streamID string) *modelStreamReader {
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
			response, errRead := s.env.ReadModelStream(context.Background(), streamID)
			result := streamReadResult{Response: response, Err: errRead}
			select {
			case reader.events <- result:
			case <-reader.ctx.Done():
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
	<-reader.done
}
