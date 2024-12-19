

build: attach-profiler libprofilers.so

.PHONY: attach-profiler
attach-profiler:
	cd ~/git/github.com/pyroscope-io/dotnetdiag/examples/attach-profiler && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o $(CURDIR)/attach-profiler


.PHONY: libprofilers.so
libprofilers.so:
	$(eval CONTAINER_ID :=$(shell docker build --platform linux/amd64 -t libprofilers ~/git/github.com/dr-dotnet/dr-dotnet/src/DrDotnet.Profilers && docker create libprofilers))
	docker cp $(CONTAINER_ID):/build/bin/debug/libprofilers.so .
	docker rm $(CONTAINER_ID)

.PHONY: run
run:
	./run.sh
