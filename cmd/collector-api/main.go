package main

import (
	"context"
	"flag"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi"
	"github.com/harrybrwn/config"
	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/internal/tracing"
	"github.com/sirupsen/logrus"
)

var log = logrus.StandardLogger()

const component = "collector-api"

func main() {
	var (
		conf       cmd.Config
		ctx        = context.Background()
		r          = chi.NewRouter()
		port       = 3020
		configfile string
	)
	flag.IntVar(&port, "port", port, "run the server on a different port")
	flag.StringVar(&configfile, "config", configfile, "use a different config file")
	flag.Parse()
	config.SetConfig(&conf)
	config.SetType("yaml")
	if configfile != "" {
		config.AddFilepath(configfile)
	} else {
		config.AddFile("config.yml")
		config.AddPath("/var/local/remora")
		config.AddPath(".")
	}
	config.InitDefaults()
	err := config.ReadConfig()
	if err != nil {
		log.Fatal(err)
	}
	tp, err := tracing.Provider(&conf.Tracer, component)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		tp.Shutdown(ctx)
	}()

	listenAndServe(port, r)
}

func listenAndServe(port int, h http.Handler) error {
	addr := net.JoinHostPort("", strconv.Itoa(port))
	log.WithField("address", addr).Info("starting server")
	err := http.ListenAndServe(addr, h)
	if err != nil {
		log.Error(err)
	}
	return err
}
