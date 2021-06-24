package main

import (
	"context"

	"github.com/harrybrwn/diktyo/queue"
	"github.com/harrybrwn/diktyo/web"
	"github.com/harrybrwn/diktyo/web/pb"
	"google.golang.org/protobuf/proto"
)

func newServer(q queue.Queue) *server {
	return &server{
		queue: q,
	}
}

type server struct {
	pb.UnimplementedPageFetcherServer
	queue queue.Queue
}

func (s *server) Enqueue(ctx context.Context, req *web.PageRequest) (*pb.Response, error) {
	select {
	case <-ctx.Done():
		return &pb.Response{
			Status:  pb.Status_Stopped,
			Message: "server stopped",
		}, nil
	default:
		err := enqueue(s.queue, req)
		if err != nil {
			return &pb.Response{
				Status: pb.Status_Failed, Message: err.Error()}, err
		}
		return &pb.Response{
			Status: pb.Status_Ok, Message: "request queued"}, nil
	}
}

func enqueue(q queue.Queue, req *web.PageRequest) error {
	raw, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	return q.PutKey([]byte(req.URL), raw)
}

func (s *server) Links(ctx context.Context, req *web.PageRequest) (*pb.PageResponse, error) {
	select {
	case <-ctx.Done():
		return &pb.PageResponse{Resp: &pb.Response{
			Status:  pb.Status_Stopped,
			Message: "server stopped",
		}}, nil
	default:
		p := web.NewPageFromString(req.URL, req.Depth)
		err := p.FetchCtx(ctx)
		if err != nil {
			return &pb.PageResponse{Resp: &pb.Response{
				Status:  pb.Status_Failed,
				Message: err.Error(),
			}}, err
		}

		links := make([]string, len(p.Links))
		for _, l := range p.Links {
			links = append(links, l.String())
		}
		return &pb.PageResponse{
			Links:        links,
			Depth:        req.Depth,
			URL:          req.URL,
			ResponseTime: int64(p.ResponseTime),
			Resp: &pb.Response{
				Status:  pb.Status_Ok,
				Message: "page links fetched",
			},
		}, nil
	}
}
