// Copyright 2015 Canonical Ltd.
// Licensed under the LGPLv3, see LICENCE file for details.

package bundles

import (
	"github.com/juju/bundlechanges"
	"github.com/juju/cmd"
	"github.com/juju/errors"
	"gopkg.in/juju/charm.v5"
	"gopkg.in/yaml.v1"

	"github.com/juju/juju/api"
	"github.com/juju/juju/constraints"
)

// Deploy deploys the given bundle data using the given API client.
// The deployment is not transactional, and its progress is notified using the
// given command context.
func Deploy(data *charm.BundleData, client *api.Client, ctx *cmd.Context) error {
	h := &handler{
		client: client,
		ctx:    ctx,
	}
	changeMap := map[string]func(id string, args []interface{}, results map[string]string) error{
		"addCharm":       h.addCharm,
		"deploy":         h.addService,
		"addMachines":    h.addMachine,
		"addRelation":    h.addRelation,
		"addUnit":        h.addUnit,
		"setAnnotations": h.setAnnotations,
	}
	changes := bundlechanges.FromData(data)
	results := make(map[string]string, len(changes))
	for _, change := range changes {
		f := changeMap[change.Method]
		if err := f(change.Id, change.Args, results); err != nil {
			return errors.Annotate(err, "cannot deploy bundle")
		}
	}
	return nil
}

type handler struct {
	client *api.Client
	ctx    *cmd.Context
}

// addCharm adds a charm to the environment.
func (h *handler) addCharm(id string, args []interface{}, results map[string]string) error {
	url := args[0].(string)
	curl := charm.MustParseURL(url)
	h.ctx.Infof("adding charm %q", url)
	if err := h.client.AddCharm(curl); err != nil {
		return errors.Annotate(err, "cannot add charm")
	}
	results[id] = url
	return nil
}

// addService deploys or update a service with no units. Service options are
// also set or updated.
func (h *handler) addService(id string, args []interface{}, results map[string]string) error {
	url, name, options := args[0].(string), args[1].(string), args[2].(map[string]interface{})
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	svcStatus, svcExists := status.Services[name]
	if svcExists {
		// The service is already deployed in the environment: check that its
		// charm is compatible with the one declared in the bundle.
		// TODO frankban: implement this logic.
		h.ctx.Infof("reusing service %q (charm: %s)", name, svcStatus.Charm)
	} else {
		// The service does not exist in the environment.
		h.ctx.Infof("deploying service %q (charm: %s)", name, url)
		// TODO frankban: handle service constraints in the bundle changes.
		numUnits, configYAML, cons, toMachineSpec := 0, "", constraints.Value{}, ""
		if err := h.client.ServiceDeploy(url, name, numUnits, configYAML, cons, toMachineSpec); err != nil {
			return errors.Annotate(err, "cannot deploy service")
		}
	}
	if len(options) > 0 {
		h.ctx.Infof("configuring service %q", name)
		config, err := yaml.Marshal(map[string]map[string]interface{}{name: options})
		if err != nil {
			return errors.Annotate(err, "cannot marshal service options")
		}
		if err := h.client.ServiceSetYAML(name, string(config)); err != nil {
			return errors.Annotate(err, "cannot set service options")
		}
	}
	results[id] = name
	return nil
}

// addMachine creates a new top-level machine or container in the environment.
func (h *handler) addMachine(id string, args []interface{}, results map[string]string) error {
	// h.ctx.Infof("adding machine")
	return nil
}

// addRelation creates a relationship between two services.
func (h *handler) addRelation(id string, args []interface{}, results map[string]string) error {
	// h.ctx.Infof("adding relation")
	return nil
}

// addUnit adds a single unit to a service already present in the environment.
func (h *handler) addUnit(id string, args []interface{}, results map[string]string) error {
	// h.ctx.Infof("adding unit")
	return nil
}

// setAnnotations sets annotations for a service or a machine.
func (h *handler) setAnnotations(id string, args []interface{}, results map[string]string) error {
	// h.ctx.Infof("setting annotations")
	return nil
}
