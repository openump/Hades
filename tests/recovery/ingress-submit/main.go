// Development-only probe of the real gateway immutable retry implementation.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/openump/Hades/internal/gateway"
	"github.com/openump/Hades/internal/transport"
	"net/http"
	"os"
	"time"
)

func main() {
	payload := flag.String("payload", "", "immutable raw reading file")
	endpoint := flag.String("ingress", "", "ingress URL")
	credentials := transport.Flags()
	flag.Parse()
	tls, err := transport.Client(*credentials)
	if err != nil {
		panic(err)
	}
	raw, err := os.ReadFile(*payload)
	if err != nil {
		panic(err)
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: tls}}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err = gateway.SubmitObserved(ctx, client, *endpoint, raw, func() { fmt.Println("retry: original bytes retained") }); err != nil {
		panic(err)
	}
	fmt.Println("accepted_broker_ack")
}
