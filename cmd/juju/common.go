// Copyright 2014 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package main

import (
	"fmt"
	"path"

	"code.google.com/p/go.net/publicsuffix"
	"github.com/juju/cmd"
	"github.com/juju/errors"
	"github.com/juju/persistent-cookiejar"
	"github.com/juju/utils"
	"gopkg.in/juju/charm.v5-unstable"
	"gopkg.in/juju/charm.v5-unstable/charmrepo"
	"gopkg.in/juju/charmstore.v4/csclient"
	"gopkg.in/macaroon-bakery.v0/httpbakery"
	"gopkg.in/macaroon.v1"

	"github.com/juju/juju/api"
	"github.com/juju/juju/cmd/envcmd"
	"github.com/juju/juju/environs"
	"github.com/juju/juju/environs/config"
	"github.com/juju/juju/environs/configstore"
)

// destroyPreparedEnviron destroys the environment and logs an error
// if it fails.
var destroyPreparedEnviron = destroyPreparedEnvironProductionFunc

func destroyPreparedEnvironProductionFunc(
	ctx *cmd.Context,
	env environs.Environ,
	store configstore.Storage,
	action string,
) {
	ctx.Infof("%s failed, destroying environment", action)
	if err := environs.Destroy(env, store); err != nil {
		logger.Errorf("the environment could not be destroyed: %v", err)
	}
}

var destroyEnvInfo = destroyEnvInfoProductionFunc

func destroyEnvInfoProductionFunc(
	ctx *cmd.Context,
	cfgName string,
	store configstore.Storage,
	action string,
) {
	ctx.Infof("%s failed, cleaning up the environment.", action)
	if err := environs.DestroyInfo(cfgName, store); err != nil {
		logger.Errorf("the environment jenv file could not be cleaned up: %v", err)
	}
}

// environFromName loads an existing environment or prepares a new
// one. If there are no errors, it returns the environ and a closure to
// clean up in case we need to further up the stack. If an error has
// occurred, the environment and cleanup function will be nil, and the
// error will be filled in.
var environFromName = environFromNameProductionFunc

func environFromNameProductionFunc(
	ctx *cmd.Context,
	envName string,
	action string,
	ensureNotBootstrapped func(environs.Environ) error,
) (env environs.Environ, cleanup func(), err error) {

	store, err := configstore.Default()
	if err != nil {
		return nil, nil, err
	}

	envExisted := false
	if environInfo, err := store.ReadInfo(envName); err == nil {
		envExisted = true
		logger.Warningf(
			"ignoring environments.yaml: using bootstrap config in %s",
			environInfo.Location(),
		)
	} else if !errors.IsNotFound(err) {
		return nil, nil, err
	}

	cleanup = func() {
		// Distinguish b/t removing the jenv file or tearing down the
		// environment. We want to remove the jenv file if preparation
		// was not successful. We want to tear down the environment
		// only in the case where the environment didn't already
		// exist.
		if env == nil {
			logger.Debugf("Destroying environment info.")
			destroyEnvInfo(ctx, envName, store, action)
		} else if !envExisted && ensureNotBootstrapped(env) != environs.ErrAlreadyBootstrapped {
			logger.Debugf("Destroying environment.")
			destroyPreparedEnviron(ctx, env, store, action)
		}
	}

	if env, err = environs.PrepareFromName(envName, envcmd.BootstrapContext(ctx), store); err != nil {
		return nil, cleanup, err
	}

	return env, cleanup, err
}

// resolveCharmURL resolves the given charm URL string
// by looking it up in the appropriate charm repository.
// If it is a charm store charm URL, the given csParams will
// be used to access the charm store repository.
// If it is a local charm URL, the local charm repository at
// the given repoPath will be used. The given configuration
// will be used to add any necessary attributes to the repo
// and to resolve the default series if possible.
//
// resolveCharmURL also returns the charm repository holding
// the charm.
func resolveCharmURL(curlStr string, csParams charmrepo.NewCharmStoreParams, repoPath string, conf *config.Config) (*charm.URL, charmrepo.Interface, error) {
	ref, err := charm.ParseReference(curlStr)
	if err != nil {
		return nil, nil, errors.Trace(err)
	}
	repo, err := charmrepo.InferRepository(ref, csParams, repoPath)
	if err != nil {
		return nil, nil, errors.Trace(err)
	}
	repo = config.SpecializeCharmRepo(repo, conf)
	if ref.Series == "" {
		if defaultSeries, ok := conf.DefaultSeries(); ok {
			ref.Series = defaultSeries
		}
	}
	if ref.Schema == "local" && ref.Series == "" {
		possibleURL := *ref
		possibleURL.Series = "trusty"
		logger.Errorf("The series is not specified in the environment (default-series) or with the charm. Did you mean:\n\t%s", &possibleURL)
		return nil, nil, errors.Errorf("cannot resolve series for charm: %q", ref)
	}
	curl, err := repo.Resolve(ref)
	if err != nil {
		return nil, nil, errors.Trace(err)
	}
	return curl, repo, nil
}

// addCharmViaAPI calls the appropriate client API calls to add the
// given charm URL to state. For non-public charm URLs, this function also
// handles the macaroon authorization process using the given csClient.
// The resulting charm URL of the added charm is displayed on stdout.
func addCharmViaAPI(client *api.Client, ctx *cmd.Context, curl *charm.URL, repo charmrepo.Interface, csclient *csClient) (*charm.URL, error) {
	switch curl.Schema {
	case "local":
		ch, err := repo.Get(curl)
		if err != nil {
			return nil, err
		}
		stateCurl, err := client.AddLocalCharm(curl, ch)
		if err != nil {
			return nil, err
		}
		curl = stateCurl
	case "cs":
		if err := client.AddCharm(curl); err != nil {
			if !isBakeryError(err) {
				return nil, err
			}
			m, err := csclient.authorize(curl)
			if err != nil {
				return nil, errors.Trace(err)
			}
			if err := client.AddCharmWithAuthorization(curl, m); err != nil {
				return nil, errors.Trace(err)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported charm URL schema: %q", curl.Schema)
	}
	ctx.Infof("Added charm %q to the environment.", curl)
	return curl, nil
}

// isBakeryError reports whether the given error is a discharge required or
// interaction required response.
func isBakeryError(err error) bool {
	return httpbakery.IsDischargeError(err) || httpbakery.IsInteractionError(err)
}

// charmStoreClient is called to obtain a charm store client including the
// parameters for connecting to the charm store, and helpers to save the local
// authorization cookies and to authorize non-public charm deployments.
// It is defined as a variable so it can be changed for testing purposes.
var charmStoreClient = func() (*csClient, error) {
	jar, err := cookiejar.New(&cookiejar.Options{
		PublicSuffixList: publicsuffix.List,
	})
	if err != nil {
		return nil, errors.Trace(err)
	}
	file := path.Join(utils.Home(), ".go-cookies")
	if err != nil {
		return nil, errors.Trace(err)
	}
	if err := cookiejar.LoadFromFile(jar, file); err != nil {
		return nil, errors.Trace(err)
	}
	client := httpbakery.NewHTTPClient()
	client.Jar = jar
	return &csClient{
		params: charmrepo.NewCharmStoreParams{
			HTTPClient:   client,
			VisitWebPage: httpbakery.OpenWebBrowser,
		},
		cookieFile: file,
	}, nil
}

// csClient gives access to the charm store server and provides parameters
// for connecting to the charm store.
type csClient struct {
	params     charmrepo.NewCharmStoreParams
	cookieFile string
}

// saveCookies saves the charm store HTTP client cookies to the default cookie
// file.
func (c *csClient) saveCookies() error {
	jar, ok := c.params.HTTPClient.Jar.(*cookiejar.Jar)
	if !ok {
		return errors.New("cannot retrieve the cookie jar")
	}
	return cookiejar.SaveToFile(jar, c.cookieFile)
}

// authorize acquires and return the charm store delegatable macaroon to be
// used to add the charm corresponding to the given URL.
// The macaroon is properly attenuated so that it can only be used to deploy
// the given charm URL.
func (c *csClient) authorize(curl *charm.URL) (*macaroon.Macaroon, error) {
	client := csclient.New(csclient.Params{
		URL:          c.params.URL,
		HTTPClient:   c.params.HTTPClient,
		VisitWebPage: c.params.VisitWebPage,
	})
	var m *macaroon.Macaroon
	if err := client.Get("/delegatable-macaroon", &m); err != nil {
		return nil, errors.Trace(err)
	}
	if err := m.AddFirstPartyCaveat("is-entity " + curl.String()); err != nil {
		return nil, errors.Trace(err)
	}
	return m, nil
}
