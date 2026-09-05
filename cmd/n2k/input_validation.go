package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
)

// Match the metadata and decoded-message variables supported by n2k.Filter.
var filterEnvironment = sync.OnceValues(func() (*cel.Env, error) {
	return cel.NewEnv(
		cel.Variable("pgn", cel.IntType), cel.Variable("source", cel.IntType),
		cel.Variable("priority", cel.IntType), cel.Variable("destination", cel.IntType),
		cel.Variable("msg", cel.DynType),
	)
})

func validateFilter(expression string) error {
	if strings.TrimSpace(expression) == "" {
		return nil
	}
	env, err := filterEnvironment()
	if err != nil {
		return err
	}
	ast, issues := env.Compile(expression)
	if issues != nil && issues.Err() != nil {
		return fmt.Errorf("invalid filter; use a comparison such as pgn == 127250:\n%w", issues.Err())
	}
	if ast.OutputType() != cel.BoolType && ast.OutputType() != cel.DynType {
		return fmt.Errorf("filter must produce true or false; use a comparison such as pgn == 127250")
	}
	return nil
}

func addressValidator(network string) func(string) error {
	return func(value string) error {
		host, port, err := net.SplitHostPort(value)
		if err != nil || (network == "TCP" && strings.TrimSpace(host) == "") || strings.ContainsAny(host, " \t\r\n/") {
			return fmt.Errorf("enter a %s address as host:port (for example 192.168.4.1:1457); UDP may use :1457", network)
		}
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return fmt.Errorf("%s port must be a number from 1 to 65535", network)
		}
		return nil
	}
}
