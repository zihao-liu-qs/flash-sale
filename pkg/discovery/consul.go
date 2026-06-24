package discovery

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	consulapi "github.com/hashicorp/consul/api"

	"github.com/qs-lzh/flash-sale/pkg/logger"
)

type ServiceRegistration struct {
	ID      string
	Name    string
	Address string
	Port    int
	client  *consulapi.Client
}

func NewClient(consulAddr string) (*consulapi.Client, error) {
	config := consulapi.DefaultConfig()
	config.Address = consulAddr
	return consulapi.NewClient(config)
}

func Register(client *consulapi.Client, name string, port int) (*ServiceRegistration, error) {
	addr := getLocalIP()

	reg := &ServiceRegistration{
		ID:      fmt.Sprintf("%s-%s-%d", name, addr, port),
		Name:    name,
		Address: addr,
		Port:    port,
		client:  client,
	}

	check := &consulapi.AgentServiceCheck{
		HTTP:                           fmt.Sprintf("http://%s:%d/health", addr, port),
		Interval:                       "10s",
		Timeout:                        "3s",
		DeregisterCriticalServiceAfter: "30s",
	}

	svc := &consulapi.AgentServiceRegistration{
		ID:      reg.ID,
		Name:    name,
		Address: addr,
		Port:    port,
		Check:   check,
	}

	if err := client.Agent().ServiceRegister(svc); err != nil {
		return nil, fmt.Errorf("consul register failed: %w", err)
	}

	logger.Log.Infof("Registered in Consul: %s at %s:%d", name, addr, port)
	return reg, nil
}

func (r *ServiceRegistration) Deregister() {
	if err := r.client.Agent().ServiceDeregister(r.ID); err != nil {
		logger.Log.Errorf("Consul deregister failed: %v", err)
	}
}

func getLocalIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	defer conn.Close()
	addr := conn.LocalAddr().String()
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

func ParsePort(addr string) int {
	if strings.HasPrefix(addr, ":") {
		port, _ := strconv.Atoi(addr[1:])
		return port
	}
	parts := strings.Split(addr, ":")
	if len(parts) == 2 {
		port, _ := strconv.Atoi(parts[1])
		return port
	}
	return 0
}
