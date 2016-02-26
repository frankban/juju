// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package commands

import (
	"fmt"
	"net/url"

	"github.com/juju/cmd"
	"github.com/juju/errors"
	"gopkg.in/macaroon-bakery.v1/httpbakery"

	"github.com/juju/juju/cmd/modelcmd"
)

func newGUICommand() cmd.Command {
	return modelcmd.Wrap(&guiCommand{})
}

// guiCommand opens the Juju GUI in the default browser.
type guiCommand struct {
	modelcmd.ModelCommandBase
}

const guiDoc = `
Open the Juju GUI in the default browser.
`

func (c *guiCommand) Info() *cmd.Info {
	return &cmd.Info{
		Name:    "gui",
		Purpose: "open the Juju GUI in the default browser",
		Doc:     guiDoc,
	}
}

func (c *guiCommand) Run(ctx *cmd.Context) error {
	endpoint, err := c.ConnectionEndpoint(true)
	if err != nil {
		return errors.Annotate(err, "cannot retrieve API endpoint")
	}

	root, err := c.NewAPIRoot()
	if err != nil {
		return errors.Annotate(err, "cannot retrieve API root")
	}
	defer root.Close()

	raw := fmt.Sprintf("https://%s/model/%s/gui/", root.Addr(), endpoint.ModelUUID)
	u, err := url.Parse(raw)
	if err != nil {
		return errors.Annotate(err, "cannot parse Juju GUI URL")
	}

	if err := httpbakery.OpenWebBrowser(u); err != nil {
		return errors.Annotate(err, "cannot open Juju GUI")
	}

	creds, err := c.ConnectionCredentials()
	if err != nil {
		return errors.Annotate(err, "cannot retrieve credentials")
	}
	fmt.Fprintf(ctx.Stdout, "Login credentials: %s / %s\n", creds.User, creds.Password)
	return nil
}
