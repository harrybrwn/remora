package web

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/chromedp"
	"google.golang.org/protobuf/proto"
	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/dynamicpb"
)

func TestHeadlessFetcher(t *testing.T) {
	t.Skip()
	ctx, cancel := chromedp.NewContext(context.Background())
	defer cancel()
	var nodes []*cdp.Node
	err := chromedp.Run(ctx,
		chromedp.Navigate("https://www.facebook.com/"),
		chromedp.Nodes("a[href], img[src]", &nodes),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		var l string
		switch strings.ToUpper(n.NodeName) {
		case "IMG":
			src, ok := n.Attribute("src")
			if ok {
				l = src
			}
		case "A":
			href, ok := n.Attribute("href")
			if ok {
				l = href
			}
		}
		if l[0] == '#' {
			continue
		}
		fmt.Println(l)
	}
}

func TestProtobufMisc(t *testing.T) {
	var p = PageRequest{
		URL:   "https://en.wikipedia.org/wiki/Main_Page",
		Key:   []byte("key"),
		Depth: 2,
		Retry: 5,
	}
	raw, err := proto.Marshal(&p)
	if err != nil {
		t.Fatal(err)
	}
	if raw == nil {
		t.Fatal("wat")
	}
	msg := p.ProtoReflect()

	// msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
	// 	fmt.Println(fd.FullName(), fd.Name(), v.IsValid())
	// 	return true
	// })

	// typ := dynamicpb.NewMessageType(msg.Type().Descriptor())
	dynmsg := dynamicpb.NewMessage(msg.Descriptor())
	dynmsg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		return true
	})
}
