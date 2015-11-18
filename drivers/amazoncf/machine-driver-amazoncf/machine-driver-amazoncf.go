package main

import (
	"github.com/docker/machine/drivers/amazoncf"
	"github.com/docker/machine/libmachine/drivers/plugin"
)

func main() {
	plugin.RegisterDriver(amazoncf.NewDriver("", ""))
}
