all: recompose-agent recompose-coordinator

clean:
	rm -f recompose-agent recompose-coordinator

recompose-agent:
	go build -ldflags="-s -w" -o $@ ./agent

recompose-coordinator:
	go build -ldflags="-s -w" -o $@ ./coordinator
