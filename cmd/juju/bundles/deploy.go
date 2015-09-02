// Copyright 2015 Canonical Ltd.
// Licensed under the LGPLv3, see LICENCE file for details.

package bundles

import (
	"fmt"
	"strings"

	"github.com/juju/bundlechanges"
	"github.com/juju/cmd"
	"github.com/juju/errors"
	"gopkg.in/juju/charm.v6-unstable"
	"gopkg.in/yaml.v1"

	"github.com/juju/juju/api"
	"github.com/juju/juju/apiserver/params"
	"github.com/juju/juju/constraints"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/state/multiwatcher"
)

// Deploy deploys the given bundle using the given API client.
// The deployment is not transactional, and its progress is notified using the
// given command context.
func Deploy(bundle charm.Bundle, client *api.Client, ctx *cmd.Context) error {
	data := bundle.Data()
	// TODO frankban: provide a verifyConstraints function.
	if err := data.Verify(nil); err != nil {
		return errors.Trace(err)
	}

	changes := bundlechanges.FromData(data)
	h := &handler{
		changes: make(map[string]bundlechanges.Change, len(changes)),
		results: make(map[string]string, len(changes)),
		client:  client,
		ctx:     ctx,
		data:    data,
	}
	for _, change := range changes {
		h.changes[change.Id()] = change
	}

	ctx.Infof("starting bundle deployment")
	var err error
	for _, change := range changes {
		switch change := change.(type) {
		case *bundlechanges.AddCharmChange:
			h.addCharm(change.Id(), change.Args)
		case *bundlechanges.AddMachineChange:
			h.addMachine(change.Id(), change.Args)
		case *bundlechanges.AddRelationChange:
			h.addRelation(change.Id(), change.Args)
		case *bundlechanges.AddServiceChange:
			h.addService(change.Id(), change.Args)
		case *bundlechanges.AddUnitChange:
			h.addUnit(change.Id(), change.Args)
		case *bundlechanges.SetAnnotationsChange:
			h.setAnnotations(change.Id(), change.Args)
		default:
			return errors.New(fmt.Sprintf("unknown change type: %T", change))
		}
		if err != nil {
			return errors.Annotate(err, "cannot deploy bundle")
		}
	}
	ctx.Infof("bundle deployment completed")
	return nil
}

type handler struct {
	changes map[string]bundlechanges.Change
	results map[string]string
	client  *api.Client
	ctx     *cmd.Context
	data    *charm.BundleData
}

// addCharm adds a charm to the environment.
func (h *handler) addCharm(id string, args bundlechanges.AddCharmArgs) error {
	url := charm.MustParseURL(args.Charm)
	h.ctx.Infof("adding charm %s", args.Charm)
	if err := h.client.AddCharm(url); err != nil {
		return errors.Annotate(err, "cannot add charm")
	}
	h.results[id] = args.Charm
	return nil
}

// addService deploys or update a service with no units. Service options are
// also set or updated.
func (h *handler) addService(id string, args bundlechanges.AddServiceArgs) error {
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	svcStatus, svcExists := status.Services[args.Service]
	if svcExists {
		// The service is already deployed in the environment: check that its
		// charm is compatible with the one declared in the bundle. If it is,
		// reuse the existing service, otherwise, deploy this service with
		// another name.
		// TODO frankban: implement this logic.
		h.ctx.Infof("reusing service %s (charm: %s)", args.Service, svcStatus.Charm)
	} else {
		// The service does not exist in the environment.
		h.ctx.Infof("deploying service %s (charm: %s)", args.Service, args.Charm)
		// TODO frankban: handle service constraints in the bundle changes.
		numUnits, configYAML, cons, toMachineSpec := 0, "", constraints.Value{}, ""
		if err := h.client.ServiceDeploy(args.Charm, args.Service, numUnits, configYAML, cons, toMachineSpec); err != nil {
			return errors.Annotate(err, "cannot deploy service")
		}
	}
	if len(args.Options) > 0 {
		h.ctx.Infof("configuring service %s", args.Service)
		config, err := yaml.Marshal(map[string]map[string]interface{}{args.Service: args.Options})
		if err != nil {
			return errors.Annotate(err, "cannot marshal service options")
		}
		if err := h.client.ServiceSetYAML(args.Service, string(config)); err != nil {
			return errors.Annotate(err, "cannot set service options")
		}
	}
	h.results[id] = args.Service
	return nil
}

// addMachine creates a new top-level machine or container in the environment.
func (h *handler) addMachine(id string, args bundlechanges.AddMachineArgs) error {
	// Check whether the desired number of units already exist in the
	// environment, in which case, avoid adding other machines to host those
	// service units.
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	change := h.serviceChangeForMachine(id)
	service := h.results[change.Id()]
	existing := len(status.Services[service].Units)
	want := h.data.Services[change.Args.Service].NumUnits
	if existing >= want {
		h.ctx.Infof("not creating another machine to host %s unit: %d unit(s) already present", service, existing)
		h.results[id] = ""
		return nil
	}
	p := params.AddMachineParams{
		// TODO frankban: add constraints here.
		Series:   args.Series,
		ParentId: args.ParentId,
		Jobs:     []multiwatcher.MachineJob{multiwatcher.JobHostUnits},
	}
	if args.ContainerType == "" {
		h.ctx.Infof("creating new machine for holding %s unit", service)
	} else {
		containerType, err := instance.ParseContainerType(args.ContainerType)
		if err != nil {
			return errors.Annotate(err, "cannot create machine")
		}
		p.ContainerType = containerType
		if p.ParentId == "" {
			h.ctx.Infof("creating %s container in new machine for holding %s unit", args.ContainerType, service)
		} else {
			p.ParentId = resolve(p.ParentId, h.results)
			h.ctx.Infof("creating %s container in machine %s for holding %s unit", args.ContainerType, p.ParentId, service)
		}
	}
	r, err := h.client.AddMachines([]params.AddMachineParams{p})
	if err != nil {
		return errors.Annotate(err, "cannot create machine")
	}
	if r[0].Error != nil {
		return errors.Trace(r[0].Error)
	}
	h.results[id] = r[0].Machine
	return nil
}

// addRelation creates a relationship between two services.
func (h *handler) addRelation(id string, args bundlechanges.AddRelationArgs) error {
	ep1, ep2 := parseEndpoint(args.Endpoint1, h.results), parseEndpoint(args.Endpoint2, h.results)
	// Check whether the given relation already exists.
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	// TODO frankban: do the check below in a better way.
	for _, r := range status.Relations {
		if len(r.Endpoints) != 2 {
			continue
		}
		if (r.Endpoints[0].String() == ep1.String() && r.Endpoints[1].String() == ep2.String()) ||
			(r.Endpoints[1].String() == ep1.String() && r.Endpoints[0].String() == ep2.String()) {
			h.ctx.Infof("%s and %s are already related", ep1, ep2)
			return nil
		}
	}
	h.ctx.Infof("relating %s and %s", ep1, ep2)
	if _, err := h.client.AddRelation(ep1.String(), ep2.String()); err != nil {
		return errors.Annotate(err, "cannot add relation")
	}
	return nil
}

// addUnit adds a single unit to a service already present in the environment.
func (h *handler) addUnit(id string, args bundlechanges.AddUnitArgs) error {
	// Check whether the desired number of units already exist in the
	// environment, in which case, avoid adding other units.
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	service := resolve(args.Service, h.results)
	change := h.changes[args.Service[1:]]
	bundleService := change.(*bundlechanges.AddServiceChange).Args.Service
	existing := len(status.Services[service].Units)
	want := h.data.Services[bundleService].NumUnits
	if existing >= want {
		h.ctx.Infof("not adding new units to service %s: %d unit(s) already present", service, existing)
		return nil
	}
	machineSpec := ""
	if args.To != "" {
		machineSpec = resolve(args.To, h.results)
		h.ctx.Infof("adding %s unit to machine %s", service, machineSpec)
	} else {
		h.ctx.Infof("adding %s unit to new machine", service)
	}
	r, err := h.client.AddServiceUnits(service, 1, machineSpec)
	if err != nil {
		return errors.Annotate(err, "cannot add service units")
	}
	// Retrieve the machine on which the unit has been deployed.
	status, err = h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	h.results[id] = status.Services[service].Units[r[0]].Machine
	return nil
}

// setAnnotations sets annotations for a service or a machine.
func (h *handler) setAnnotations(id string, args bundlechanges.SetAnnotationsArgs) error {
	// TODO frankban: implement this.
	return nil
}

// serviceChangeForMachine returns the "addService" change for which an
// addMachine change is required. Receive the id of the addMachine change.
// Adding machines is required to place units, and units belong to services.
func (h *handler) serviceChangeForMachine(id string) *bundlechanges.AddServiceChange {
	var change bundlechanges.Change
mainloop:
	for _, change = range h.changes {
		for _, required := range change.Requires() {
			if required == id {
				break mainloop
			}
		}
	}
	switch change := change.(type) {
	case *bundlechanges.AddMachineChange:
		// The original machine is a container, and its parent is another
		// "addMachines" change. Search again using the parent id.
		return h.serviceChangeForMachine(change.Id())
	case *bundlechanges.AddUnitChange:
		// We have found the "addUnit" change, which refers to the service: now
		// look for the original "addService" change.
		id = change.Args.Service[1:]
		return h.changes[id].(*bundlechanges.AddServiceChange)
	}
	panic("unreachable")
}

// resolve returns the real entity name for the bundle entity (for instance a
// service or a machine) with the given placeholder id.
func resolve(placeholder string, results map[string]string) string {
	id := placeholder[1:]
	return results[id]
}

// parseEndpoint creates an endpoint from its string representation in e.
func parseEndpoint(e interface{}, results map[string]string) *endpoint {
	parts := strings.SplitN(e.(string), ":", 2)
	ep := &endpoint{
		service: resolve(parts[0], results),
	}
	if len(parts) == 2 {
		ep.relation = parts[1]
	}
	return ep
}

// endpoint holds a relation endpoint.
type endpoint struct {
	service  string
	relation string
}

// String returns the string representation of an endpoint.
func (ep endpoint) String() string {
	if ep.relation == "" {
		return ep.service
	}
	return fmt.Sprintf("%s:%s", ep.service, ep.relation)
}
