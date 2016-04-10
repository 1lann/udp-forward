# udp-forward

A dead simple Go (golang) package to forward UDP packets like a reverse NAT (i.e. it supports multiple users).

## Usage

```go
package main

import "github.com/1lann/udp-forward"

func main() {
	// Forward(src, dst)
	forwarder, err := forward.Forward("0.0.0.0:1000", "1.2.3.4:1023")
	if err != nil {
		panic(err)
	}

	// Do something...

	// Stop the forwarder
	forwarder.Close()
}
```

See the [GoDoc](https://godoc.org/github.com/1lann/udp-forward) for documentation.

## License

There is [no license](/LICENSE).
