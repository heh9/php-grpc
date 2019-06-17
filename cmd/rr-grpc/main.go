package main

import (
	"time"
	"os"

	"github.com/sirupsen/logrus"
	"github.com/spiral/php-grpc"
	"github.com/spiral/roadrunner/service/rpc"
	"github.com/opentracing/opentracing-go"
	"github.com/uber/jaeger-client-go/config"

	rr "github.com/spiral/roadrunner/cmd/rr/cmd"

	// grpc specific commands
	_ "github.com/spiral/php-grpc/cmd/rr-grpc/grpc"
)

func main() {
	t, closer, err := config.Configuration{
		ServiceName: os.Getenv("JAEGER_SERVICE"),
		Sampler: &config.SamplerConfig{
			Type:  "const",
			Param: 1,
		},
		Reporter: &config.ReporterConfig{
			BufferFlushInterval: 1 * time.Second,
			LocalAgentHostPort:  os.Getenv("JAEGER_ADDRESS"),
		},
	}.NewTracer()
	if err != nil {
		logrus.Fatal(err)
	}
	defer closer.Close()
	opentracing.SetGlobalTracer(t)

	rr.Container.Register(rpc.ID, &rpc.Service{})
	rr.Container.Register(grpc.ID, &grpc.Service{})

	rr.Logger.Formatter = &logrus.TextFormatter{ForceColors: true}
	rr.Execute()
}
