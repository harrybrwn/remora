package visitor

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	"github.com/harrybrwn/diktyo/web"
)

type FSVisitor struct {
	Base  string
	Hosts map[string]struct{}
	mu    sync.Mutex
}

func (fsv *FSVisitor) Visit(ctx context.Context, p *web.Page) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	if p.Doc == nil {
		return
	}
	u := *p.URL
	u.Scheme = ""
	filename := filepath.Join(fsv.Base, u.String())
	dir, _ := filepath.Split(filename)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		err = os.MkdirAll(dir, 0777)
		if err != nil {
			log.WithError(err).Error("could not create directory")
			return
		}
	}
	file, err := os.OpenFile(filename, os.O_TRUNC|os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		log.WithError(err).Error("could not open file")
		return
	}
	defer file.Close()
	stat, err := file.Stat()
	if err != nil {
		return
	}
	if stat.IsDir() {
		os.MkdirAll(filename, 0777)
		return
	}
	err = writePage(file, p)
	if err != nil {
		log.WithError(err).Error("could not write to file")
		return
	}
}

func writePage(w io.Writer, p *web.Page) error {
	body, err := p.Doc.Html()
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, body)
	return err
}

func (fsv *FSVisitor) Filter(p *web.PageRequest, u *url.URL) error {
	fsv.mu.Lock()
	_, ok := fsv.Hosts[u.Host]
	fsv.mu.Unlock()
	if ok {
		return nil
	}
	return web.ErrSkipURL
}

func (fsv *FSVisitor) LinkFound(u *url.URL) {}
