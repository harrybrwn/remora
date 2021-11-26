package visitor

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"syscall"

	"github.com/harrybrwn/remora/web"
	"github.com/pkg/errors"
)

type FSVisitor struct {
	Base  string
	Hosts map[string]struct{}
	mu    sync.Mutex
}

func NewFS(base string, hosts ...string) *FSVisitor {
	v := FSVisitor{
		Base:  base,
		Hosts: make(map[string]struct{}),
	}
	for _, h := range hosts {
		v.Hosts[h] = struct{}{}
	}
	return &v
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

func (fsv *FSVisitor) LinkFound(u *url.URL) error { return nil }

func savePage(basedir string, p *web.Page) error {
	var (
		err error
		u   = *p.URL
	)
	u.Scheme = ""
	filename := filepath.Join(basedir, u.String())
	dir, _ := filepath.Split(filename)
	if _, err = os.Stat(dir); os.IsNotExist(err) {
		err = os.MkdirAll(dir, 0777)
		if err != nil {
			return errors.Wrap(err, "could not create directory")
		}
	}
	movethis, yes := hasFileInPathname(filename)
	if yes {
		err = mv(movethis, filepath.Join(filepath.Dir(movethis), "index.html"))
		if err != nil {
			return errors.Wrap(err, "could not move file that needs moving")
		}
	}
	stat, err := os.Stat(filename)
	if !os.IsNotExist(err) && err != nil {
		return nil
	}
	if stat != nil {
		if stat.IsDir() {
			log.WithField("file", filename).Warn("file is a directory")
			return nil
		}
	}

	file, err := os.OpenFile(
		filename,
		os.O_TRUNC|os.O_WRONLY|os.O_CREATE,
		0644,
	)
	if errors.Is(err, syscall.EISDIR) {
		file, err = os.OpenFile(
			filepath.Join(filename, "index.html"),
			os.O_TRUNC|os.O_WRONLY|os.O_CREATE,
			0644,
		)
	}
	if err != nil {
		return errors.Wrap(err, "could not open file")
	}
	defer file.Close()
	// stat, err := file.Stat()
	// if err != nil {
	// 	// return errors.Wrap(err, "could not stat file")
	// 	log.WithError(err).Warn("could not stat file")
	// 	return nil
	// }
	// if stat.IsDir() {
	// 	return os.MkdirAll(filename, 0777)
	// }
	err = writePage(file, p)
	if err != nil {
		return errors.Wrap(err, "could not write to file")
	}
	log.Info(filename)
	return nil
}

func (fsv *FSVisitor) Visit(ctx context.Context, p *web.Page) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	err := savePage(fsv.Base, p)
	if err != nil {
		log.WithError(err).Error("save page failed")
	}
}

func mv(from, to string) error {
	orig, err := os.OpenFile(from, os.O_RDONLY, 0644)
	if err != nil {
		return err
	}
	defer orig.Close()
	nw, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return err
	}
	defer nw.Close()
	if _, err = io.Copy(nw, orig); err != nil {
		return err
	}
	return os.Remove(from)
}

func hasFileInPathname(filename string) (string, bool) {
	switch filename {
	case "/", "./", ".", "":
		return "", false
	}
	stat, err := os.Stat(filename)
	if err != nil {
		return hasFileInPathname(filepath.Dir(filename))
	}
	return filename, !stat.IsDir()
}

func writePage(w io.Writer, p *web.Page) error {
	if p.Doc == nil {
		_, err := io.Copy(w, p.Response.Body)
		return err
	}
	body, err := p.Doc.Html()
	if err != nil {
		return err
	}
	_, err = io.WriteString(w, body)
	return err
}
