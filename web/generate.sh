#!/bin/sh

set -eu

mkdir -p ./webpb
protoc \
	-I../protobuf \
	--go_out=paths=source_relative:./webpb \
	--go_opt=Mpage_request.proto=github.com/harrybrwn/remora/web/webpb \
	--go-grpc_out=paths=source_relative:./webpb \
	--go-grpc_opt=Mpage_request.proto=github.com/harrybrwn/remora/web/webpb \
	page.proto page_request.proto

# Legacy: Generate a PageRequest for the web package too. This is really messy
# and should probably be fixed in the future:
#
# TODO: Choose between ./web and ./web/webpb for the proto package.
protoc \
	--proto_path=../protobuf \
	--go_out=paths=source_relative:. \
	--go_opt=Mpage_request.proto=github.com/harrybrwn/remora/web \
 	--go-grpc_out=. \
	--go-grpc_opt=paths=source_relative \
	--go-grpc_opt=Mpage_request.proto=github.com/harrybrwn/remora/web \
	page_request.proto

