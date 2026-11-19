// Command reactorcide-egress-net sets up the network of a job so that all
// of its egress goes through the Reactorcide egress proxy. See package netd.
package main

import (
	"os"

	"github.com/catalystcommunity/reactorcide/egress_proxy/netd"
)

func main() { os.Exit(netd.Main(os.Args[1:])) }
