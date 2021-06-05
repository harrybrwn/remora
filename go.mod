module github.com/harrybrwn/diktyo

go 1.16

require (
	github.com/PuerkitoBio/goquery v1.6.1
	github.com/dgraph-io/badger/v3 v3.2011.1
	github.com/fatih/color v1.7.0
	github.com/harrybrwn/config v0.1.2
	github.com/joho/godotenv v1.3.0
	github.com/lib/pq v1.10.2
	github.com/pkg/errors v0.9.1
	github.com/sirupsen/logrus v1.8.1
	github.com/spf13/cobra v1.1.3
	github.com/spf13/pflag v1.0.5
	github.com/streadway/amqp v1.0.0
	golang.org/x/net v0.0.0-20210521195947-fe42d452be8f
	golang.org/x/sync v0.0.0-20201020160332-67f06af15bc9
	gopkg.in/natefinch/lumberjack.v2 v2.0.0
)

replace github.com/harrybrwn/config => ../../pkg/config
