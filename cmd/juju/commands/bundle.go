// Copyright 2015 Canonical Ltd.
// Licensed under the LGPLv3, see LICENCE file for details.

package commands

import (
	"fmt"
	"strings"

	"github.com/juju/bundlechanges"
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

// deploymentLogger is used to notify clients about the bundle deployment
// progress.
type deploymentLogger interface {
	// Infof formats and logs the given message.
	Infof(string, ...interface{})
}

// deployBundle deploys the given bundle data using the given API client and
// charm store client. The deployment is not transactional, and its progress is
// notified using the given deployment logger.
func deployBundle(data *charm.BundleData, client *api.Client, csclient *csClient, repoPath string, conf *config.Config, log deploymentLogger) error {
	if err := data.Verify(func(s string) error {
		_, err := constraints.Parse(s)
		return err
	}); err != nil {
		return errors.Annotate(err, "cannot deploy bundle")
	}

	// Retrieve bundle changes.
	changes := bundlechanges.FromData(data)
	h := &bundleHandler{
		changes:  make(map[string]bundlechanges.Change, len(changes)),
		results:  make(map[string]string, len(changes)),
		client:   client,
		csclient: csclient,
		repoPath: repoPath,
		conf:     conf,
		log:      log,
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
			return errors.Errorf("unknown change type: %T", change)
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
	log      deploymentLogger
	data     *charm.BundleData
}

// addCharm adds a charm to the environment.
func (h *bundleHandler) addCharm(id string, p bundlechanges.AddCharmParams) error {
	url, repo, err := resolveCharmStoreEntityURL(p.Charm, h.csclient.params, h.repoPath, h.conf)
	if err != nil {
		return errors.Annotatef(err, "cannot resolve URL %q", p.Charm)
	}
	if url.Series == "bundle" {
		return errors.Errorf("expected charm URL, got bundle URL %q", p.Charm)
	}
	url, err = addCharmViaAPI(h.client, url, repo, h.csclient)
	if err != nil {
		return errors.Annotatef(err, "cannot add charm %q", p.Charm)
	}
	h.log.Infof("added charm %s", url)
	// TODO frankban: the key here should really be the change id, but in the
	// current bundlechanges format the charm name is included in the service
	// change, not a placeholder pointing to the corresponding charm change, as
	// it should be instead.
	h.results["resolved-"+p.Charm] = url.String()
	return nil
}

// addService deploys or update a service with no units. Service options are
// also set or updated.
func (h *bundleHandler) addService(id string, p bundlechanges.AddServiceParams) error {
	// TODO frankban: the charm should really be resolved using
	// resolve(p.Charm, h.results) at this point: see TODO in addCharm.
	ch := h.results["resolved-"+p.Charm]
	// TODO frankban: handle service constraints in the bundle changes.
	numUnits, configYAML, cons, toMachineSpec := 0, "", constraints.Value{}, ""
	if err := h.client.ServiceDeploy(ch, p.Service, numUnits, configYAML, cons, toMachineSpec); err == nil {
		h.log.Infof("service %s deployed (charm: %s)", p.Service, ch)
		// TODO frankban (bug 1495952): do this check using the cause rather
		// than the string when a specific cause is available.
	} else if strings.HasSuffix(err.Error(), "service already exists") {
		// The service is already deployed in the environment: check that its
		// charm is compatible with the one declared in the bundle. If it is,
		// reuse the existing service or upgrade to a specified revision.
		// Exit with an error otherwise.
		if err := upgradeCharm(h.client, h.log, p.Service, ch); err != nil {
			return errors.Annotatef(err, "cannot upgrade service %q", p.Service)
		}
	} else {
		return errors.Annotatef(err, "cannot deploy service %q", p.Service)
	}
	if len(p.Options) > 0 {
		if err := setServiceOptions(h.client, p.Service, p.Options); err != nil {
			return errors.Trace(err)
		}
		h.log.Infof("service %s configured", p.Service)
	}
	h.results[id] = p.Service
	return nil
}

// addMachine creates a new top-level machine or container in the environment.
func (h *bundleHandler) addMachine(id string, p bundlechanges.AddMachineParams) error {
	// Check whether the desired number of units already exist in the
	// environment, in which case, avoid adding other machines to host those
	// service units.
	service := h.serviceForMachineChange(id)
	existingUnits, err := existingUnitsForService(h.client, service)
	if err != nil {
		return errors.Annotatef(err, "cannot get existing units for service %q", service)
	}
	numExisting := len(existingUnits)
	numWant := h.data.Services[service].NumUnits
	if numExisting >= numWant {
		h.log.Infof("avoid creating another machine to host %s unit: %s", service, existingUnitsMessage(numExisting))
		// We still need to set the machine used to add this unit, as
		// subsequent changes can depend on this one. Using one of the machines
		// hosting units for the current service is out best guess.
		for _, machine := range existingUnits {
			h.results[id] = machine
			break
		}
		return nil
	}
	cons, err := constraints.Parse(p.Constraints)
	if err != nil {
		// This should never happen, as the bundle is already verified.
		return errors.Annotate(err, "invalid constraints for machine")
	}
	machineParams := params.AddMachineParams{
		Constraints: cons,
		Series:      p.Series,
		Jobs:        []multiwatcher.MachineJob{multiwatcher.JobHostUnits},
	}
	if p.ContainerType != "" {
		containerType, err := instance.ParseContainerType(p.ContainerType)
		if err != nil {
			return errors.Annotatef(err, "cannot create machine for hosting %q unit", service)
		}
		machineParams.ContainerType = containerType
		if p.ParentId != "" {
			machineParams.ParentId = resolve(p.ParentId, h.results)
		}
	}
	r, err := h.client.AddMachines([]params.AddMachineParams{machineParams})
	if err != nil {
		return errors.Annotatef(err, "cannot create machine for hosting %q unit", service)
	}
	if r[0].Error != nil {
		return errors.Trace(r[0].Error)
	}
	machine := r[0].Machine
	if p.ContainerType == "" {
		h.log.Infof("created new machine %s for holding %s unit", machine, service)
	} else if p.ParentId == "" {
		h.log.Infof("created %s container in new machine for holding %s unit", machine, service)
	} else {
		h.log.Infof("created %s container in machine %s for holding %s unit", machine, machineParams.ParentId, service)
	}
	h.results[id] = machine
	return nil
}

// addRelation creates a relationship between two services.
func (h *bundleHandler) addRelation(id string, p bundlechanges.AddRelationParams) error {
	ep1 := resolveRelation(p.Endpoint1, h.results)
	ep2 := resolveRelation(p.Endpoint2, h.results)
	_, err := h.client.AddRelation(ep1, ep2)
	if err == nil {
		// A new relation has been established.
		h.log.Infof("related %s and %s", ep1, ep2)
		return nil
	}
	// TODO frankban (bug 1495952): do this check using the cause rather than
	// the string when a specific cause is available.
	if strings.HasSuffix(err.Error(), "relation already exists") {
		// The relation is already present in the environment.
		h.log.Infof("%s and %s are already related", ep1, ep2)
		return nil
	}
	return errors.Annotatef(err, "cannot add relation between %q and %q", ep1, ep2)
}

// addUnit adds a single unit to a service already present in the environment.
func (h *bundleHandler) addUnit(id string, p bundlechanges.AddUnitParams) error {
	// Check whether the desired number of units already exist in the
	// environment, in which case, avoid adding other units.
	service := resolve(p.Service, h.results)
	existingUnits, err := existingUnitsForService(h.client, service)
	if err != nil {
		return errors.Annotatef(err, "cannot get existing units for service %q", service)
	}
	numExisting := len(existingUnits)
	numWant := h.data.Services[service].NumUnits
	if numExisting >= numWant {
		h.log.Infof("avoid adding new unit to service %s: %s", service, existingUnitsMessage(numExisting))
		return nil
	}
	// Note that resolving the machine could fail (and therefore return an
	// empty string) in the case the bundle is deployed a second time and some
	// units are missing. In such cases, just create new machines.
	machineSpec := ""
	if p.To != "" {
		machineSpec = resolve(p.To, h.results)
	}
	r, err := h.client.AddServiceUnits(service, 1, machineSpec)
	if err != nil {
		return errors.Annotatef(err, "cannot add unit for service %q", service)
	}
	unit := r[0]
	if machineSpec == "" {
		// Retrieve the machine on which the unit has been deployed.
		existingUnits, err = existingUnitsForService(h.client, service)
		if err != nil {
			return errors.Annotatef(err, "cannot get existing units for service %q", service)
		}
		machineSpec = existingUnits[unit]
		h.log.Infof("added %s unit to new machine %s", unit, machineSpec)
	} else {
		h.log.Infof("added %s unit to existing machine %s", unit, machineSpec)
	}
	h.results[id] = machineSpec
	return nil
}

// setAnnotations sets annotations for a service or a machine.
func (h *bundleHandler) setAnnotations(id string, p bundlechanges.SetAnnotationsParams) error {
	// TODO frankban: implement this method.
	return nil
}

// upgradeCharm upgrades the charm for the given service to the given charm id.
// If the service is already deployed using the given charm id, do nothing.
// This function returns an error if the existing charm and the target one are
// incompatible, meaning an upgrade from one to the other is not allowed.
func upgradeCharm(client *api.Client, log deploymentLogger, service, id string) error {
	existing, err := client.ServiceGetCharmURL(service)
	if err != nil {
		return errors.Annotatef(err, "cannot retrieve info for service %q", service)
	}
	if existing.String() == id {
		log.Infof("reusing service %s (charm: %s)", service, id)
		return nil
	}
	url, err := charm.ParseURL(id)
	if err != nil {
		return errors.Annotatef(err, "cannot parse charm URL %q", id)
	}
	if url.WithRevision(-1).Path() != existing.WithRevision(-1).Path() {
		return errors.Errorf("bundle charm %q is incompatible with existing charm %q", id, existing)
	}
	if err := client.ServiceSetCharm(service, id, false); err != nil {
		return errors.Annotatef(err, "cannot upgrade charm to %q", id)
	}
	log.Infof("upgraded charm for existing service %s (from %s to %s)", service, existing, id)
	return nil
}

// setServiceOptions changes the configuration for the given service.
func setServiceOptions(client *api.Client, service string, options map[string]interface{}) error {
	config, err := yaml.Marshal(map[string]map[string]interface{}{service: options})
	if err != nil {
		return errors.Annotatef(err, "cannot marshal options for service %q", service)
	}
	if err := client.ServiceSetYAML(service, string(config)); err != nil {
		return errors.Annotatef(err, "cannot set options for service %q", service)
	}
	return nil
}

// resolve returns the real entity name for the bundle entity (for instance a
// service or a machine) with the given placeholder id.
// A placeholder id is a string like "$deploy-42" or "$addCharm-2", indicating
// the results of a previously applied change. It always starts with a dollar
// sign, followed by the identifier of the referred change. A change id is a
// string indicating the action type ("deploy", "addRelation" etc.), followed
// by a unique incremental number.
func resolve(placeholder string, results map[string]string) string {
	if !strings.HasPrefix(placeholder, "$") {
		panic(`placeholder does not start with "$"`)
	}
	id := placeholder[1:]
	return results[id]
}

// resolveRelation returns the relation name resolving the included service
// placeholder.
func resolveRelation(e string, results map[string]string) string {
	parts := strings.SplitN(e, ":", 2)
	service := resolve(parts[0], results)
	if len(parts) == 1 {
		return service
	}
	return fmt.Sprintf("%s:%s", service, parts[1])
}

// existingUnitsForService returns existing units for the given service name.
// Units are returned as a map mapping unit names to the corresponding machine
// names.
func existingUnitsForService(client *api.Client, service string) (map[string]string, error) {
	status, err := client.Status(nil)
	if err != nil {
		return nil, errors.Annotate(err, "cannot retrieve environment status")
	}
	units := status.Services[service].Units
	results := make(map[string]string, len(units))
	for name, unit := range units {
		results[name] = unit.Machine
	}
	return results, nil
}

// existingUnitsMessage returns a string message stating that the given number
// of units already exist in the environment.
func existingUnitsMessage(num int) string {
	if num == 1 {
		return "1 unit already present"
	}
	return fmt.Sprintf("%d units already present", num)
}

// serviceForMachineChange returns the name of the service for which an
// "addMachine" change is required. Receive the id of the "addMachine" change.
// Adding machines is required to place units, and units belong to services.
func (h *bundleHandler) serviceForMachineChange(id string) string {
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
		return h.serviceForMachineChange(change.Id())
	case *bundlechanges.AddUnitChange:
		// We have found the "addUnit" change, which refers to the service: now
		// resolve the service holding the unit.
		return resolve(change.Params.Service, h.results)
	default:
		panic(fmt.Sprintf("unexpected change %T", change))
	}
	panic("unreachable")
}
