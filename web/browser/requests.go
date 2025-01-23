package browser

import (
	"context"
	"fmt"
	"net/url"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/sirupsen/logrus"
)

type Request struct {
	*network.Request
	Initiator *network.Initiator
	Response  *Response
}

type Response struct {
	*network.Response
	Request *Request
}

type RequestState struct {
	Response *Response
	PageURL  string
	Type     network.ResourceType
}

type RequestOption func(*requestOpts)

type requestOpts struct {
	logger       *logrus.Logger
	resourceType network.ResourceType
}

func WithLogger(l *logrus.Logger) RequestOption { return func(ro *requestOpts) { ro.logger = l } }
func WithResourceType(rt network.ResourceType) RequestOption {
	return func(ro *requestOpts) { ro.resourceType = rt }
}

func (ro *requestOpts) defaults() {
	if len(ro.resourceType) == 0 {
		ro.resourceType = DefaultResourceType
	}
	if ro.logger == nil {
		ro.logger = logrus.StandardLogger()
	}
}

func RequestDo(state *RequestState, opts ...RequestOption) chromedp.Action {
	var options requestOpts
	options.defaults()
	for _, o := range opts {
		o(&options)
	}
	return responseAction(state.PageURL, state, nil, &options)
}

func RunRequest(
	ctx context.Context,
	actions ...chromedp.Action,
) (*RequestState, error) {
	var (
		state RequestState
		opts  requestOpts
	)
	opts.defaults()
	err := chromedp.Run(ctx, responseAction(
		"",
		&state,
		actions,
		&opts,
	))
	if err != nil {
		return nil, err
	}
	return &state, nil
}

// Navigate will asynchronously navigate to the given url.
func Navigate(uri *url.URL) chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		_, _, _, err := page.Navigate(uri.String()).Do(ctx)
		return err
	})
}

func responseAction(
	pageURL string,
	requests *RequestState,
	actions []chromedp.Action,
	options *requestOpts,
) chromedp.Action {
	requests.PageURL = pageURL
	requests.Type = options.resourceType
	return chromedp.ActionFunc(func(ctx context.Context) (err error) {
		loadCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		state := pageState{
			state:  requests,
			cancel: cancel,
			// frames: &frames,
		}
		var loadErr error
		frames, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		state.frameID = frames.Frame.ID

		chromedp.ListenTarget(loadCtx, func(event any) {
			if state.loaderID != "" {
				loadErr = handleEvent(&state, event)
				if loadErr != nil {
					cancel()
					state.finished = true
				}
				return
			}
			// fmt.Printf("early event: %#v\n", event)
			switch ev := event.(type) {
			case *page.EventFrameNavigated:
				// Make sure we keep frameID up to date.
				if ev.Frame.ParentID == "" {
					state.frameID = ev.Frame.ID
				}
			case *network.EventRequestWillBeSent:
				// Under some circumstances like ERR_TOO_MANY_REDIRECTS, we never
				// see the "init" lifecycle event we want. Those "lone" requests
				// also tend to have a loaderID that matches their requestID, for
				// some reason. If such a request is seen, use it.
				// TODO: research this some more when we have the time.
				if ev.FrameID == state.frameID && string(ev.LoaderID) == string(ev.RequestID) {
					state.loaderID = ev.LoaderID
					state.addRequest(ev)
				}
			case *page.EventLifecycleEvent:
				if ev.FrameID == state.frameID && ev.Name == "init" {
					state.loaderID = ev.LoaderID
				}
			case *page.EventNavigatedWithinDocument:
				// A fragment navigation doesn't need extra steps.
				state.finished = true
				cancel()
			}
			// if loaderID != "" {
			// 	for _, ev := range earlyEvents {
			// 		handleEvent(ev)
			// 	}
			// 	earlyEvents = nil
			// }
		})

		if err = chromedp.Run(ctx, actions...); err != nil {
			return err
		}

		select {
		case <-loadCtx.Done():
			if loadErr != nil {
				return loadErr
			}
			// If the ctx parameter was cancelled by the caller (or
			// by a timeout etc.) the select will race between
			// lctx.Done and ctx.Done, since lctx is a sub-context
			// of ctx. So we can't return nil here, as otherwise
			// that race would mean that we would drop 50% of the
			// parent context cancellation errors.
			if !state.finished {
				return ctx.Err()
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
}

type pageState struct {
	state    *RequestState
	frameID  cdp.FrameID
	loaderID cdp.LoaderID
	reqID    network.RequestID
	hasInit  bool
	finished bool
	cancel   context.CancelFunc
	prevReq  *Request
	// frames   *page.FrameTree
}

func (ps *pageState) urlMatches(uri string) bool {
	// PageURL should be optional so we return true if it doesn't exist
	if len(ps.state.PageURL) == 0 {
		return true
	}
	return uri == ps.state.PageURL
}

func handleEvent(rs *pageState, event any) (err error) {
	switch ev := event.(type) {
	// case *page.EventFrameAttached:
	// 	fmt.Printf("frame attatched: %#v\n", ev)
	// case *page.EventFrameStartedLoading:
	// 	fmt.Printf("frame started loading: %#v\n", ev)
	// 	rs.frameID = ev.FrameID
	// 	// chromedp.GetFrameTree()

	case *network.EventRequestWillBeSent:
		if ev.Type != rs.state.Type {
			break
		}
		if ev.FrameID != rs.frameID {
			fmt.Printf("request frameID mismatch: %#v loader(%v) req(%q)\n", ev.DocumentURL, rs.loaderID, rs.reqID)
			break
		}

		fmt.Printf("FrameID: %v, LoaderID: %v request (%q) -> %s\n", ev.FrameID, ev.LoaderID, ev.RequestID, ev.DocumentURL)
		// TODO consider removing this, we're keeping the whole chain of redirects why keep the first documentURL?
		if ev.RedirectResponse == nil && rs.prevReq == nil && rs.urlMatches(ev.DocumentURL) {
			rs.state.PageURL = ev.DocumentURL
		}
		if ev.LoaderID == rs.loaderID {
			rs.reqID = ev.RequestID
		}
		rs.addRequest(ev)
	case *network.EventResponseReceived:
		if ev.Type != rs.state.Type {
			break
		}
		if ev.FrameID != rs.frameID {
			// fmt.Printf("response frame mismatch(%q): %v != %v\n", ev.Response.URL, ev.FrameID, rs.frameID)
			break
		}
		if ev.RequestID != rs.reqID {
			// fmt.Printf("response requestId mismatch: %v != %v\n", ev.RequestID, rs.reqID)
			break
		}
		fmt.Printf("FrameID: %v, LoaderID: %v response(%q) -> %v\n", ev.FrameID, ev.LoaderID, rs.reqID, ev.Response.URL)
		rs.state.Response = &Response{
			Response: ev.Response,
			Request:  rs.prevReq,
		}
	case *page.EventLifecycleEvent:
		if ev.FrameID == rs.frameID && ev.Name == "init" {
			rs.hasInit = true
		}
	case *page.EventLoadEventFired:
		// Ignore load events before the "init"
		// lifecycle event, as those are old.
		if rs.hasInit {
			rs.finished = true
			rs.cancel()
		}
	case *network.EventLoadingFailed:
		if ev.RequestID == rs.reqID {
			err = fmt.Errorf("page load error %s", ev.ErrorText)
			// If Canceled is true, we won't receive a
			// loadEventFired at all.
			if ev.Canceled {
				rs.finished = true
				rs.cancel()
			}
		}
	}
	return err
}

func (ps *pageState) addRequest(event *network.EventRequestWillBeSent) {
	req := &Request{Request: event.Request, Initiator: event.Initiator}
	if event.RedirectResponse != nil && ps.prevReq != nil {
		req.Response = &Response{Response: event.RedirectResponse, Request: ps.prevReq}
	}
	ps.prevReq = req
}

func getFrames(frames *page.FrameTree) []chromedp.Action {
	return []chromedp.Action{chromedp.ActionFunc(func(ctx context.Context) error {
		fmt.Println("calling getFrames")
		tree, err := page.GetFrameTree().Do(ctx)
		if err != nil {
			return err
		}
		*frames = *tree
		return nil
	})}
}
