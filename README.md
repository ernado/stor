# stor

[![codecov](https://codecov.io/gh/ernado/stor/graph/badge.svg?token=ULW7VAOOSP)](https://codecov.io/gh/ernado/stor)
[![e2e](https://github.com/ernado/stor/actions/workflows/e2e.yml/badge.svg)](https://github.com/ernado/stor/actions/workflows/e2e.yml)

Toy file storage. Not for production use.

https://github.com/user-attachments/assets/c61de5dc-f518-4c28-b4e3-695c06838fbf

![stor.svg](stor.svg)

```mermaid
sequenceDiagram
    Note over node, front: register
    node->>front: register
    front->>ydb: register
    ydb-->>front: ok
    front-->>node: ok

    actor user
    Note over user, front: upload
    user->>front: upload
    front-->front: split file into chunks
    front->>ydb: get nodes
    ydb-->>front: nodes
    front->>ydb: create file
    front->>ydb: create chunks
    front->>node: upload chunks
    node-->>front: ok
    ydb-->>front: ok
    front-->>user: upload link

    Note over user, front: download
    user->>front: download
    front->>ydb: get chunks
    ydb-->>front: chunks
    front->>node: download chunks
    node-->>front: chunks
    front-->front: assemble file
    front-->>user: file
```

## Running

Run with observability stack:

```bash
docker compose --profile full up -d
```

Grafana dashboard is available at http://localhost:3000/d/stor/stor.

Run with minimal setup:
```bash
docker compose --profile app up -d
```

### Checking

See `./cmd/stor-upload`.

```bash
stor-upload --help
Usage of stor-upload:
  -check
    	download and check file checksum
  -file string
    	file to upload
  -gen
    	generate random file to temp dir
  -gen-size string
    	generate file of given size (default "100M")
  -name string
    	name of the file (defaults to file base name)
  -rnd
    	use random prefix for the file name
  -server-url string
    	server URL (default "http://localhost:8080")
```

```console
$ go run ./cmd/stor-upload -gen -gen-size 1GB --check
uploading 100% |██████████████████████████████████████████| (1.0/1.0 GB, 850 MB/s)
uploaded link: http://localhost:8080/download/stor-upload-501649240.bin
computing original sha256 100% |██████████████████████████| (1.0/1.0 GB, 2.0 GB/s)
original sha256: 3e6d6d836b6298d3540df54da03bf6c4d980a749890abcf9af6f199f58ff0d18
downloading 100% |████████████████████████████████████████| (1.0/1.0 GB, 1.6 GB/s)
downloaded sha256: 3e6d6d836b6298d3540df54da03bf6c4d980a749890abcf9af6f199f58ff0d18
checksum match
```

## Cleanup

```
docker compose --profile full down --timeout 1 --volumes
```
