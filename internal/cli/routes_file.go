package cli

import (
	"errors"
	"fmt"
	"io"
	"os"

	"go.yaml.in/yaml/v3"
)

// routesFile is the layout of the --routes YAML file.
type routesFile struct {
	Routes []routeInFile `yaml:"routes"`
}

// routeInFile is one route as written in the file. QoS is a pointer so a missing qos can be
// told apart from qos: 0.
type routeInFile struct {
	Name   string `yaml:"name"`
	Filter string `yaml:"filter"`
	Stream string `yaml:"stream"`
	Prefix string `yaml:"prefix"`
	QoS    *int   `yaml:"qos"`
}

// loadRoutesFile reads the routes from a YAML file and fills in the defaults: the stream name
// is the route name, and QoS is 1. Unknown keys are an error, so a typo doesn't silently fall
// back to a default. The routes are checked later, together with routes from flags.
func loadRoutesFile(path string) ([]Route, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening routes file: %w", err)
	}
	defer func() { _ = file.Close() }()

	decoder := yaml.NewDecoder(file)
	decoder.KnownFields(true)

	var contents routesFile
	if err := decoder.Decode(&contents); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("reading routes file %s: %w", path, err)
	}

	routes := make([]Route, 0, len(contents.Routes))
	for _, inFile := range contents.Routes {
		route := Route{
			Name:   inFile.Name,
			Filter: inFile.Filter,
			Stream: inFile.Stream,
			Prefix: inFile.Prefix,
			QoS:    1,
		}

		if route.Stream == "" {
			route.Stream = route.Name
		}

		if inFile.QoS != nil {
			route.QoS = *inFile.QoS
		}

		routes = append(routes, route)
	}

	return routes, nil
}
