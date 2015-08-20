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
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/constraints"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/state/multiwatcher"
)

// Deploy deploys the given bundle data using the given API client.
// The deployment is not transactional, and its progress is notified using the
// given command context.
func Deploy(data *charm.BundleData, client *api.Client, ctx *cmd.Context) error {
	changes := bundlechanges.FromData(data)
	h := &handler{
		changes: make(map[string]*bundlechanges.Change, len(changes)),
		client:  client,
		ctx:     ctx,
		data:    data,
	}
	for _, change := range changes {
		h.changes[change.Id] = change
	}
	changeMap := map[string]func(id string, args []interface{}, results map[string]string) error{
		"addCharm":       h.addCharm,
		"deploy":         h.addService,
		"addMachines":    h.addMachine,
		"addRelation":    h.addRelation,
		"addUnit":        h.addUnit,
		"setAnnotations": h.setAnnotations,
	}

	results := make(map[string]string, len(changes))
	for _, change := range changes {
		f := changeMap[change.Method]
		if err := f(change.Id, change.Args, results); err != nil {
			return errors.Annotate(err, "cannot deploy bundle")
		}
	}
	ctx.Infof("bundle deployment completed")
	return nil
}

type handler struct {
	changes map[string]*bundlechanges.Change
	client  *api.Client
	ctx     *cmd.Context
	data    *charm.BundleData
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
	url, service, options := args[0].(string), args[1].(string), args[2].(map[string]interface{})
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	svcStatus, svcExists := status.Services[service]
	if svcExists {
		// The service is already deployed in the environment: check that its
		// charm is compatible with the one declared in the bundle. If it is,
		// reuse the existing service, otherwise, deploy this service with
		// another name.
		// TODO frankban: implement this logic.
		h.ctx.Infof("reusing service %q (charm: %s)", service, svcStatus.Charm)
	} else {
		// The service does not exist in the environment.
		h.ctx.Infof("deploying service %q (charm: %s)", service, url)
		// TODO frankban: handle service constraints in the bundle changes.
		numUnits, configYAML, cons, toMachineSpec := 0, "", constraints.Value{}, ""
		if err := h.client.ServiceDeploy(url, service, numUnits, configYAML, cons, toMachineSpec); err != nil {
			return errors.Annotate(err, "cannot deploy service")
		}
	}
	if len(options) > 0 {
		h.ctx.Infof("configuring service %q", service)
		config, err := yaml.Marshal(map[string]map[string]interface{}{service: options})
		if err != nil {
			return errors.Annotate(err, "cannot marshal service options")
		}
		if err := h.client.ServiceSetYAML(service, string(config)); err != nil {
			return errors.Annotate(err, "cannot set service options")
		}
	}
	results[id] = service
	return nil
}

// addMachine creates a new top-level machine or container in the environment.
func (h *handler) addMachine(id string, args []interface{}, results map[string]string) error {
	// Check whether the desired number of units already exist in the
	// environment, in which case, avoid adding other machines to host those
	// service units.
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	change := h.serviceChangeForMachine(id)
	service := results[change.Id]
	bundleService := change.Args[1].(string)
	existing := len(status.Services[service].Units)
	want := h.data.Services[bundleService].NumUnits
	if existing >= want {
		h.ctx.Infof("not adding another machine to host a %s unit: %d unit(s) already present", service, existing)
		results[id] = ""
		return nil
	}
	options := args[0].(map[string]string)
	ctype := options["containerType"]
	p := params.AddMachineParams{
		// TODO frankban: add constraints here.
		Series:   options["series"],
		ParentId: options["parentId"],
		Jobs:     []multiwatcher.MachineJob{multiwatcher.JobHostUnits},
	}
	if ctype == "" {
		h.ctx.Infof("adding a new machine for holding %s unit", service)
	} else {
		containerType, err := instance.ParseContainerType(ctype)
		if err != nil {
			return errors.Annotate(err, "cannot add machine")
		}
		p.ContainerType = containerType
		if p.ParentId == "" {
			h.ctx.Infof("adding %q container to a new machine for holding %s unit", ctype, service)
		} else {
			p.ParentId = resolve(p.ParentId, results)
			h.ctx.Infof("adding %q container to machine %q for holding %s unit", ctype, p.ParentId, service)
		}
	}
	r, err := h.client.AddMachines([]params.AddMachineParams{p})
	if err != nil {
		return errors.Annotate(err, "cannot add machine")
	}
	if r[0].Error != nil {
		return errors.Trace(r[0].Error)
	}
	results[id] = r[0].Machine
	return nil
}

// addRelation creates a relationship between two services.
func (h *handler) addRelation(id string, args []interface{}, results map[string]string) error {
	// TODO frankban: implement this.
	return nil
}

// addUnit adds a single unit to a service already present in the environment.
func (h *handler) addUnit(id string, args []interface{}, results map[string]string) error {
	// Check whether the desired number of units already exist in the
	// environment, in which case, avoid adding other units.
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	placeholder := args[0].(string)
	service := resolve(placeholder, results)
	change := h.changes[placeholder[1:]]
	bundleService := change.Args[1].(string)
	existing := len(status.Services[service].Units)
	want := h.data.Services[bundleService].NumUnits
	if existing >= want {
		h.ctx.Infof("not adding new units to service %s: %d unit(s) already present", service, existing)
		return nil
	}
	numUnits := args[1].(int)
	machineSpec := ""
	// TODO frankban: improve numUnit handling.
	if numUnits == 1 && args[2] != nil {
		machineSpec = resolve(args[2].(string), results)
		h.ctx.Infof("adding 1 unit for service %s to machine %s", service, machineSpec)
	} else {
		h.ctx.Infof("adding %d units for service %s", numUnits, service)
	}
	r, err := h.client.AddServiceUnits(service, numUnits, machineSpec)
	if err != nil {
		return errors.Annotate(err, "cannot add service units")
	}
	if numUnits != 1 {
		return nil
	}
	// Retrieve the machine on which the unit has been deployed.
	status, err = h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	results[id] = status.Services[service].Units[r[0]].Machine
	return nil
}

// setAnnotations sets annotations for a service or a machine.
func (h *handler) setAnnotations(id string, args []interface{}, results map[string]string) error {
	// TODO frankban: implement this.
	return nil
}

// serviceChangeForMachine returns the "addService" change for which an
// addMachine change is required. Receive the id of the addMachine change.
// Adding machines is required to place units, and units belong to services.
func (h *handler) serviceChangeForMachine(id string) *bundlechanges.Change {
	var change *bundlechanges.Change
mainloop:
	for _, change = range h.changes {
		for _, required := range change.Requires {
			if required == id {
				break mainloop
			}
		}
	}
	if change.Method == "addMachines" {
		// The original machine is a container, and its parent is another
		// "addMachines" change. Search again using the parent id.
		return h.serviceChangeForMachine(change.Id)
	}
	// We have found the "addUnit" change, which refers to the service: now
	// look for the original "addService" change.
	id = change.Args[0].(string)[1:]
	return h.changes[id]
}

// resolve returns the real entity name for the bundle entity (for instance a
// service or a machine) with the given placeholder id.
func resolve(placeholder string, results map[string]string) string {
	id := placeholder[1:]
	return results[id]
}
