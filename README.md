# Remora

A Remora is a type of fish that has a symbiotic relationship with sharks or
other large fish where they will attach themselves to the side of the host shark
and will remove dead skin and parasites. This is an equally beneficial
relationship because the remora gains protection by clinging to the shark. This
relationship is analogous to the relationship between web crawlers and the web
as a whole.

## Configuration

```yaml
# Logging level, any of trace, debug, info, warning, error, or fatal
loglevel: debug

# Default wait time for any host
sleep: 1s125ms

# DB is where all of the Postgres
# config information goes
db:
  host: localhost
  port: 5432
  user: me
  password: password1
  name: db_name

# RabbitMQ config information
message_queue:
  host: localhost
  port: 5672
  # RabbitMQ gives the option of running a managment server
  # along side the message queue.
  management:
    port: 15672
  prefetch: 10 # default message prefetch

# The visited set is a Redis server
# used for storing visited URLs
visited_set:
  host: 0.0.0.0
  port: 6379

# This is a map that allows wait times to be
# specified for individual host names
wait_times:
  en.wikiedia.org: 100ms
  www.goodreads.com: 250ms

# Seed URLs
seeds:
  - https://en.wikipedia.org/wiki/Main_Page
  - https://www.goodreads.com/
```

## Deployment

Make sure the database, redis, and rabbitmq services are running and they have been
added to the main configuration file (see [configuration](#configuration)). Then
run `make all` to build the deployment program. Make sure that each machine can
access the config file, which I usually do by creating a volume called
`remora-config` on each machine and uploading the file to it.

Create a `deployment.yml` configuration file and then run the deploy program in
`./cmd/deploy`.

Here is an example of a `deployment.yml` config.

```yaml
# Volumes for all instances
volumes: ["remora-config:/var/local/remora"]

# Instances for each host
hosts:
  - host: 10.0.0.1
    image: remora:0.1
    volumes: ["/usr/local/remora-datastore:/remora-datastore"]
    instances:
      - name: wiki,
        command:
          - "spider"
          - "--host"
          - "en.wikipedia.org",
          - "--prefetch=12"
          - "--sleep=50ms"
      - { name: goodreads, command: ["spider", "--host", "www.goodreads.com", "--host", "www.goodreads.com"] }
      - { name: journals,  command: ["spider", "--host", "doi.org", "--host", "www.sciencemag.org", "--host", "www.acm.org"] }
      - { name: pew,       command: ["spider", "--host", "www.pewresearch.org", "--host", "www.goodreads.com"] }
  - host: 10.0.0.2
    image: remora:0.1
    command: ["spider", "--host", "en.wikipedia.org"]
    instances:
      - { name: wiki,   command: ["spider", "--host", "en.wikipedia.org"] }
      - { name: acm,    command: ["spider", "--host", "technews.acm.org", "--host", "www.acm.org"]    }
      - { name: quotes, command: ["spider", "--host", "quotes.toscrape.com"] }
  - host: 10.0.0.3
    image: remora:0.1
    instances:
      - { name: wiki,   command: ["spider", "--host", "en.wikipedia.org"] }
      - { name: npr,    command: ["spider", "--host", "www.npr.org"]           }
      - { name: nature, command: ["spider", "--host", "www.nature.com"]        }

build:
  image: remora:0.1
  context: .
  dockerfile: ./Dockerfile

# Additional build targets are listed here
builds:
  - host: unix:///var/run/docker.sock # build on local machine
  - host: 10.0.0.201 # build on some remote machine
```

Once your configuration is done you can run this command to start the crawlers.

```sh
bin/deploy up
```

And this command to stop them.

```sh
bin/deploy down
```

To add a seed url to the queue run the command

```sh
remora enqueue [seed urls...]
```

For the example deployment configuration above a good seed is
<https://en.wikipedia.org/wiki/Main_Page> but you can add whatever url you like
and as many as you like with the `remora enqueue` command.

## Building

Install the [protocol buffer compiler](https://grpc.io/docs/protoc-installation/).

Then Install the two go extensions for protoc.

```sh
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.27.1
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.1.0
```

Install mockgen

```
go install github.com/golang/mock/mockgen@latest
```

And finally, compile either by hand or with make.

```
go generate ./...
go build ./cmd/remora
go build -o ./bin/deploy ./cmd/deploy
```

```
make
```
