package testrunner

import (
	"fmt"
	"os/exec"
	"time"

	"github.com/tedsuo/ifrit/ginkgomon"
)

type Args struct {
	Address           string
	ConfigFilePath    string
	StartExternalPort int
}

func (args Args) ArgSlice() []string {
	return []string{
		"-address=" + args.Address,
		"-tcpLoadBalancerConfig=" + args.ConfigFilePath,
		"-startExternalPort=" + fmt.Sprintf("%d", args.StartExternalPort),
	}
}

func New(binPath string, args Args) *ginkgomon.Runner {
	return ginkgomon.New(ginkgomon.Config{
		Name:              "router-configurer",
		AnsiColorCode:     "1;97m",
		StartCheck:        "router-configurer.started",
		StartCheckTimeout: 10 * time.Second,
		Command:           exec.Command(binPath, args.ArgSlice()...),
	})
}
