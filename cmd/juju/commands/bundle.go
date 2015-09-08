// Copyright 2015 Canonical Ltd.
// Licensed under the LGPLv3, see LICENCE file for details.

package commands

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
	"github.com/juju/juju/environs/config"
	"github.com/juju/juju/instance"
	"github.com/juju/juju/state/multiwatcher"
)

// deployBundle deploys the given bundle using the given API client and charm
// store client. The deployment is not transactional, and its progress is
// notified using the given command context.
func deployBundle(bundle charm.Bundle, client *api.Client, csclient *csClient, repoPath string, conf *config.Config, ctx *cmd.Context) error {
	return deployBundleData(bundle.Data(), client, csclient, repoPath, conf, ctx)
}

// deployBundleData deploys the given bundle data using the given API client
// and charm store client. The deployment is not transactional, and its
// progress is notified using the given command context.
func deployBundleData(data *charm.BundleData, client *api.Client, csclient *csClient, repoPath string, conf *config.Config, ctx *cmd.Context) error {
	// TODO frankban: provide a verifyConstraints function.
	if err := data.Verify(nil); err != nil {
		return errors.Trace(err)
	}

	// Retrueve bundle changes.
	changes := bundlechanges.FromData(data)
	h := &bundleHandler{
		changes:  make(map[string]bundlechanges.Change, len(changes)),
		results:  make(map[string]string, len(changes)),
		client:   client,
		csclient: csclient,
		repoPath: repoPath,
		conf:     conf,
		ctx:      ctx,
		data:     data,
	}
	for _, change := range changes {
		h.changes[change.Id()] = change
	}

	// Deploy the bundle.
	var err error
	for _, change := range changes {
		switch change := change.(type) {
		case *bundlechanges.AddCharmChange:
			err = h.addCharm(change.Id(), change.Params)
		case *bundlechanges.AddMachineChange:
			err = h.addMachine(change.Id(), change.Params)
		case *bundlechanges.AddRelationChange:
			err = h.addRelation(change.Id(), change.Params)
		case *bundlechanges.AddServiceChange:
			err = h.addService(change.Id(), change.Params)
		case *bundlechanges.AddUnitChange:
			err = h.addUnit(change.Id(), change.Params)
		case *bundlechanges.SetAnnotationsChange:
			err = h.setAnnotations(change.Id(), change.Params)
		default:
			return errors.New(fmt.Sprintf("unknown change type: %T", change))
		}
		if err != nil {
			return errors.Annotate(err, "cannot deploy bundle")
		}
	}
	return nil
}

type bundleHandler struct {
	changes  map[string]bundlechanges.Change
	results  map[string]string
	client   *api.Client
	csclient *csClient
	repoPath string
	conf     *config.Config
	ctx      *cmd.Context
	data     *charm.BundleData
}

// addCharm adds a charm to the environment.
func (h *bundleHandler) addCharm(id string, p bundlechanges.AddCharmParams) error {
	url, repo, err := resolveEntityURL(p.Charm, h.csclient.params, h.repoPath, h.conf)
	if err != nil {
		return errors.Annotatef(err, "cannot resolve URL %q", p.Charm)
	}
	if url.Series == "bundle" {
		return errors.New(fmt.Sprintf("expected charm URL, got bundle URL %q", p.Charm))
	}
	h.ctx.Infof("adding charm %s", url)
	url, err = addCharmViaAPI(h.client, url, repo, h.csclient)
	if err != nil {
		return errors.Annotatef(err, "cannot add charm %q", url)
	}
	// TODO frankban: the key here should really be the change id, but in the
	// current bundlechanges format the charm name is included in the service
	// change, not a placeholder pointing to the corresponding charm change, as
	// it should be instead.
	h.results[p.Charm] = url.String()
	return nil
}

// addService deploys or update a service with no units. Service options are
// also set or updated.
func (h *bundleHandler) addService(id string, p bundlechanges.AddServiceParams) error {
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	svcStatus, svcExists := status.Services[p.Service]
	if svcExists {
		// The service is already deployed in the environment: check that its
		// charm is compatible with the one declared in the bundle. If it is,
		// reuse the existing service, otherwise, deploy this service with
		// another name.
		// TODO frankban: implement this logic.
		h.ctx.Infof("reusing service %s (charm: %s)", p.Service, svcStatus.Charm)
	} else {
		// The service does not exist in the environment.
		// TODO frankban: the charm should really be resolved using
		// resolve(p.Charm, h.results) at this point: see TODO in addCharm.
		charm := h.results[p.Charm]
		h.ctx.Infof("deploying service %s (charm: %s)", p.Service, charm)
		// TODO frankban: handle service constraints in the bundle changes.
		numUnits, configYAML, cons, toMachineSpec := 0, "", constraints.Value{}, ""
		if err := h.client.ServiceDeploy(charm, p.Service, numUnits, configYAML, cons, toMachineSpec); err != nil {
			return errors.Annotatef(err, "cannot deploy service %q", p.Service)
		}
	}
	if len(p.Options) > 0 {
		h.ctx.Infof("configuring service %s", p.Service)
		config, err := yaml.Marshal(map[string]map[string]interface{}{p.Service: p.Options})
		if err != nil {
			return errors.Annotatef(err, "cannot marshal options for service %q", p.Service)
		}
		if err := h.client.ServiceSetYAML(p.Service, string(config)); err != nil {
			return errors.Annotatef(err, "cannot set options for service %q", p.Service)
		}
	}
	h.results[id] = p.Service
	return nil
}

// addMachine creates a new top-level machine or container in the environment.
func (h *bundleHandler) addMachine(id string, p bundlechanges.AddMachineParams) error {
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
	want := h.data.Services[change.Params.Service].NumUnits
	if existing >= want {
		h.ctx.Infof("not creating another machine to host %s unit: %s", service, existingUnitsMessage(existing))
		// This is our best guess for the resulting machine id.
		for _, u := range status.Services[service].Units {
			h.results[id] = u.Machine
			break
		}
		return nil
	}
	machineParams := params.AddMachineParams{
		// TODO frankban: add constraints here.
		Series:   p.Series,
		ParentId: p.ParentId,
		Jobs:     []multiwatcher.MachineJob{multiwatcher.JobHostUnits},
	}
	if p.ContainerType == "" {
		h.ctx.Infof("creating new machine for holding %s unit", service)
	} else {
		containerType, err := instance.ParseContainerType(p.ContainerType)
		if err != nil {
			return errors.Annotate(err, "cannot create machine")
		}
		machineParams.ContainerType = containerType
		if machineParams.ParentId == "" {
			h.ctx.Infof("creating %s container in new machine for holding %s unit", p.ContainerType, service)
		} else {
			machineParams.ParentId = resolve(machineParams.ParentId, h.results)
			h.ctx.Infof("creating %s container in machine %s for holding %s unit", p.ContainerType, machineParams.ParentId, service)
		}
	}
	r, err := h.client.AddMachines([]params.AddMachineParams{machineParams})
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
func (h *bundleHandler) addRelation(id string, p bundlechanges.AddRelationParams) error {
	ep1, ep2 := parseEndpoint(p.Endpoint1, h.results), parseEndpoint(p.Endpoint2, h.results)
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
		return errors.Annotatef(err, "cannot add relation between %q and %q", ep1, ep2)
	}
	return nil
}

// addUnit adds a single unit to a service already present in the environment.
func (h *bundleHandler) addUnit(id string, p bundlechanges.AddUnitParams) error {
	// Check whether the desired number of units already exist in the
	// environment, in which case, avoid adding other units.
	status, err := h.client.Status(nil)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve environment status")
	}
	service := resolve(p.Service, h.results)
	change := h.changes[p.Service[1:]]
	bundleService := change.(*bundlechanges.AddServiceChange).Params.Service
	existing := len(status.Services[service].Units)
	want := h.data.Services[bundleService].NumUnits
	if existing >= want {
		h.ctx.Infof("not adding new units to service %s: %s", service, existingUnitsMessage(existing))
		return nil
	}
	// Note that resolving the machine could fail (and therefore return an
	// empty string) in the case the bundle is deployed a second time and some
	// units are missing. In such cases, just create new machines.
	machineSpec := ""
	if p.To != "" {
		machineSpec = resolve(p.To, h.results)
		h.ctx.Infof("adding %s unit to machine %s", service, machineSpec)
	} else {
		h.ctx.Infof("adding %s unit to new machine", service)
	}
	r, err := h.client.AddServiceUnits(service, 1, machineSpec)
	if err != nil {
		return errors.Annotatef(err, "cannot add unit for service %q", service)
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
func (h *bundleHandler) setAnnotations(id string, p bundlechanges.SetAnnotationsParams) error {
	// TODO frankban: implement this.
	return nil
}

// serviceChangeForMachine returns the "addService" change for which an
// addMachine change is required. Receive the id of the addMachine change.
// Adding machines is required to place units, and units belong to services.
func (h *bundleHandler) serviceChangeForMachine(id string) *bundlechanges.AddServiceChange {
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
		id = change.Params.Service[1:]
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

// existingUnitsMessage returns a string message stating that the given number
// of units already exist in the environment.
func existingUnitsMessage(num int) string {
	if num == 1 {
		return "1 unit already present"
	}
	return fmt.Sprintf("%d units already present", num)
}

// parseEndpoint creates an endpoint from its string representation in e.
func parseEndpoint(e interface{}, results map[string]string) *relationEndpoint {
	parts := strings.SplitN(e.(string), ":", 2)
	ep := &relationEndpoint{
		service: resolve(parts[0], results),
	}
	if len(parts) == 2 {
		ep.relation = parts[1]
	}
	return ep
}

// relationEndpoint holds a relation endpoint.
type relationEndpoint struct {
	service  string
	relation string
}

// String returns the string representation of an endpoint.
func (ep relationEndpoint) String() string {
	if ep.relation == "" {
		return ep.service
	}
	return fmt.Sprintf("%s:%s", ep.service, ep.relation)
}
