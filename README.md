# webshadow
Agent-friendly web generator

## HAR benchmark

`webshadow bench` measures how well an agent turns a recorded browser session
(HAR) into a shadow that another agent can use to complete a task. See
[benchmarks/README.md](benchmarks/README.md) for the scenario format.

```
go run ./cmd/webshadow bench validate benchmarks/
go test ./...
```
