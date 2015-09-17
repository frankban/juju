// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package commands

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"time"

	jc "github.com/juju/testing/checkers"
	gc "gopkg.in/check.v1"
	"gopkg.in/juju/charm.v6-unstable"

	"github.com/juju/juju/cmd/envcmd"
	"github.com/juju/juju/constraints"
	"github.com/juju/juju/juju/testing"
	"github.com/juju/juju/state/watcher"
	"github.com/juju/juju/testcharms"
	coretesting "github.com/juju/juju/testing"
)

// runDeployCommand executes the deploy command in order to deploy the given
// charm or bundle. The deployment output and error are returned.
func runDeployCommand(c *gc.C, id string) (string, error) {
	ctx, err := coretesting.RunCommand(c, envcmd.Wrap(&DeployCommand{}), id)
	return strings.Trim(coretesting.Stderr(ctx), "\n"), err
}

func (s *DeploySuite) TestDeployBundleNotFoundLocal(c *gc.C) {
	err := runDeploy(c, "local:bundle/no-such")
	c.Assert(err, gc.ErrorMatches, `entity not found in ".*": local:bundle/no-such-0`)
}

func (s *DeployCharmStoreSuite) TestDeployBundeNotFoundCharmStore(c *gc.C) {
	err := runDeploy(c, "bundle/no-such")
	c.Assert(err, gc.ErrorMatches, `cannot resolve URL "cs:bundle/no-such": bundle not found`)
}

func (s *DeployCharmStoreSuite) TestDeployBundleSuccess(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/mysql-42", "mysql")
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-47", "wordpress")
	testcharms.UploadBundle(c, s.client, "bundle/wordpress-simple-1", "wordpress-simple")
	output, err := runDeployCommand(c, "bundle/wordpress-simple")
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/mysql-42
service mysql deployed (charm: cs:trusty/mysql-42)
added charm cs:trusty/wordpress-47
service wordpress deployed (charm: cs:trusty/wordpress-47)
related wordpress:db and mysql:server
added mysql/0 unit to new machine
added wordpress/0 unit to new machine
deployment of bundle "cs:bundle/wordpress-simple-1" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertCharmsUplodaded(c, "cs:trusty/mysql-42", "cs:trusty/wordpress-47")
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"mysql":     {charm: "cs:trusty/mysql-42"},
		"wordpress": {charm: "cs:trusty/wordpress-47"},
	})
	s.assertRelationsEstablished(c, "wordpress:db mysql:server")
	s.assertUnitsCreated(c, map[string]string{
		"mysql/0":     "0",
		"wordpress/0": "1",
	})
}

func (s *DeployCharmStoreSuite) TestDeployBundleTwice(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/mysql-42", "mysql")
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-47", "wordpress")
	testcharms.UploadBundle(c, s.client, "bundle/wordpress-simple-1", "wordpress-simple")
	_, err := runDeployCommand(c, "bundle/wordpress-simple")
	c.Assert(err, jc.ErrorIsNil)
	output, err := runDeployCommand(c, "bundle/wordpress-simple")
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/mysql-42
reusing service mysql (charm: cs:trusty/mysql-42)
added charm cs:trusty/wordpress-47
reusing service wordpress (charm: cs:trusty/wordpress-47)
wordpress:db and mysql:server are already related
avoid adding new units to service mysql: 1 unit already present
avoid adding new units to service wordpress: 1 unit already present
deployment of bundle "cs:bundle/wordpress-simple-1" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertCharmsUplodaded(c, "cs:trusty/mysql-42", "cs:trusty/wordpress-47")
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"mysql":     {charm: "cs:trusty/mysql-42"},
		"wordpress": {charm: "cs:trusty/wordpress-47"},
	})
	s.assertRelationsEstablished(c, "wordpress:db mysql:server")
	s.assertUnitsCreated(c, map[string]string{
		"mysql/0":     "0",
		"wordpress/0": "1",
	})
}

func (s *DeployCharmStoreSuite) TestDeployBundleGatedCharm(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/mysql-42", "mysql")
	url, _ := testcharms.UploadCharm(c, s.client, "trusty/wordpress-47", "wordpress")
	s.changeReadPerm(c, url, clientUserName)
	testcharms.UploadBundle(c, s.client, "bundle/wordpress-simple-1", "wordpress-simple")
	_, err := runDeployCommand(c, "bundle/wordpress-simple")
	c.Assert(err, jc.ErrorIsNil)
	s.assertCharmsUplodaded(c, "cs:trusty/mysql-42", "cs:trusty/wordpress-47")
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"mysql":     {charm: "cs:trusty/mysql-42"},
		"wordpress": {charm: "cs:trusty/wordpress-47"},
	})
}

func (s *DeployCharmStoreSuite) TestDeployBundleGatedCharmUnauthorized(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/mysql-42", "mysql")
	url, _ := testcharms.UploadCharm(c, s.client, "trusty/wordpress-47", "wordpress")
	s.changeReadPerm(c, url, "who")
	testcharms.UploadBundle(c, s.client, "bundle/wordpress-simple-1", "wordpress-simple")
	_, err := runDeployCommand(c, "bundle/wordpress-simple")
	c.Assert(err, gc.ErrorMatches, `cannot deploy bundle: .*: unauthorized: access denied for user "client-username"`)
}

type deployRepoCharmStoreSuite struct {
	charmStoreSuite
	testing.BaseRepoSuite
}

var _ = gc.Suite(&deployRepoCharmStoreSuite{})

func (s *deployRepoCharmStoreSuite) SetUpSuite(c *gc.C) {
	s.charmStoreSuite.SetUpSuite(c)
	s.BaseRepoSuite.SetUpSuite(c)

}

func (s *deployRepoCharmStoreSuite) TearDownSuite(c *gc.C) {
	s.BaseRepoSuite.TearDownSuite(c)
	s.charmStoreSuite.TearDownSuite(c)
}

func (s *deployRepoCharmStoreSuite) SetUpTest(c *gc.C) {
	s.PatchValue(&watcher.Period, 10*time.Millisecond)
	s.charmStoreSuite.SetUpTest(c)
	s.BaseRepoSuite.SetUpTest(c)
}

func (s *deployRepoCharmStoreSuite) TearDownTest(c *gc.C) {
	s.BaseRepoSuite.TearDownTest(c)
	s.charmStoreSuite.TearDownTest(c)
	s.PatchValue(&watcher.Period, 10*time.Second)
}

// deployBundleYAML uses the given bundle content to create a bundle in the
// local repository and then deploy it. It returns the bundle deployment output
// and error.
func (s *deployRepoCharmStoreSuite) deployBundleYAML(c *gc.C, content string) (string, error) {
	bundlePath := filepath.Join(s.BundlesPath, "example")
	c.Assert(os.Mkdir(bundlePath, 0777), jc.ErrorIsNil)
	defer os.RemoveAll(bundlePath)
	err := ioutil.WriteFile(filepath.Join(bundlePath, "bundle.yaml"), []byte(content), 0644)
	c.Assert(err, jc.ErrorIsNil)
	err = ioutil.WriteFile(filepath.Join(bundlePath, "README.md"), []byte("README"), 0644)
	c.Assert(err, jc.ErrorIsNil)
	return runDeployCommand(c, "local:bundle/example")
}

var deployBundleErrorsTests = []struct {
	about   string
	content string
	err     string
}{{
	about: "local charm not found",
	content: `
        services:
            mysql:
                charm: local:mysql
                num_units: 1
    `,
	err: `cannot deploy bundle: cannot resolve URL "local:mysql": entity not found .*`,
}, {
	about: "charm store charm not found",
	content: `
        services:
            rails:
                charm: trusty/rails-42
                num_units: 1
    `,
	err: `cannot deploy bundle: cannot add charm "trusty/rails-42": cannot retrieve "cs:trusty/rails-42": charm not found`,
}, {
	about:   "invalid bundle content",
	content: "!",
	err:     `cannot unmarshal bundle data: YAML error: .*`,
}, {
	about: "invalid bundle data",
	content: `
        services:
            mysql:
                charm: mysql
                num_units: -1
    `,
	err: `cannot deploy bundle: negative number of units specified on service "mysql"`,
}, {
	about: "invalid constraints",
	content: `
        services:
            mysql:
                charm: mysql
                num_units: 1
                constraints: bad-wolf
    `,
	err: `cannot deploy bundle: invalid constraints "bad-wolf" in service "mysql": malformed constraint "bad-wolf"`,
}, {
	about: "bundle inception",
	content: `
        services:
            example:
                charm: local:bundle/example
                num_units: 1
    `,
	err: `cannot deploy bundle: expected charm URL, got bundle URL "local:bundle/example"`,
}}

func (s *deployRepoCharmStoreSuite) TestDeployBundleErrors(c *gc.C) {
	for i, test := range deployBundleErrorsTests {
		c.Logf("test %d: %s", i, test.about)
		_, err := s.deployBundleYAML(c, test.content)
		c.Assert(err, gc.ErrorMatches, test.err)
	}
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleInvalidOptions(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-42", "wordpress")
	_, err := s.deployBundleYAML(c, `
        services:
            wp:
                charm: trusty/wordpress-42
                num_units: 1
                options:
                    blog-title: 42
    `)
	c.Assert(err, gc.ErrorMatches, `cannot deploy bundle: cannot set options for service "wp": option "blog-title" expected string, got 42`)
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleInvalidMachineContainerType(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-42", "wordpress")
	_, err := s.deployBundleYAML(c, `
        services:
            wp:
                charm: trusty/wordpress-42
                num_units: 1
                to: ["bad:1"]
        machines:
            1:
    `)
	c.Assert(err, gc.ErrorMatches, `cannot deploy bundle: cannot create machine for hosting "wp" unit: invalid container type "bad"`)
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleInvalidSeries(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "vivid/django-0", "dummy")
	_, err := s.deployBundleYAML(c, `
        services:
            django:
                charm: vivid/django
                num_units: 1
                to:
                    - 1
        machines:
            1:
                series: trusty
    `)
	c.Assert(err, gc.ErrorMatches, `cannot deploy bundle: cannot add unit for service "django": cannot assign unit "django/0" to machine 0: series does not match`)
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleLocalDeployment(c *gc.C) {
	testcharms.Repo.ClonedDirPath(s.SeriesPath, "mysql")
	testcharms.Repo.ClonedDirPath(s.SeriesPath, "wordpress")
	output, err := s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: local:wordpress
                num_units: 1
            mysql:
                charm: local:mysql
                num_units: 2
        relations:
            - ["wordpress:db", "mysql:server"]
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm local:trusty/mysql-1
service mysql deployed (charm: local:trusty/mysql-1)
added charm local:trusty/wordpress-3
service wordpress deployed (charm: local:trusty/wordpress-3)
related wordpress:db and mysql:server
added mysql/0 unit to new machine
added mysql/1 unit to new machine
added wordpress/0 unit to new machine
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertCharmsUplodaded(c, "local:trusty/mysql-1", "local:trusty/wordpress-3")
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"mysql":     {charm: "local:trusty/mysql-1"},
		"wordpress": {charm: "local:trusty/wordpress-3"},
	})
	s.assertRelationsEstablished(c, "wordpress:db mysql:server")
	s.assertUnitsCreated(c, map[string]string{
		"mysql/0":     "0",
		"mysql/1":     "1",
		"wordpress/0": "2",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleLocalAndCharmStoreCharms(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-42", "wordpress")
	testcharms.Repo.ClonedDirPath(s.SeriesPath, "mysql")
	output, err := s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: trusty/wordpress-42
                num_units: 1
            mysql:
                charm: local:mysql
                num_units: 1
        relations:
            - ["wordpress:db", "mysql:server"]
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm local:trusty/mysql-1
service mysql deployed (charm: local:trusty/mysql-1)
added charm cs:trusty/wordpress-42
service wordpress deployed (charm: cs:trusty/wordpress-42)
related wordpress:db and mysql:server
added mysql/0 unit to new machine
added wordpress/0 unit to new machine
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertCharmsUplodaded(c, "local:trusty/mysql-1", "cs:trusty/wordpress-42")
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"mysql":     {charm: "local:trusty/mysql-1"},
		"wordpress": {charm: "cs:trusty/wordpress-42"},
	})
	s.assertRelationsEstablished(c, "wordpress:db mysql:server")
	s.assertUnitsCreated(c, map[string]string{
		"mysql/0":     "0",
		"wordpress/0": "1",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleServiceOptions(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-42", "wordpress")
	testcharms.UploadCharm(c, s.client, "precise/dummy-0", "dummy")
	output, err := s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: wordpress
                num_units: 1
                options:
                    blog-title: these are the voyages
            customized:
                charm: precise/dummy-0
                num_units: 1
                options:
                    username: who
                    skill-level: 47
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:precise/dummy-0
service customized deployed (charm: cs:precise/dummy-0)
service customized configured
added charm cs:trusty/wordpress-42
service wordpress deployed (charm: cs:trusty/wordpress-42)
service wordpress configured
added customized/0 unit to new machine
added wordpress/0 unit to new machine
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertCharmsUplodaded(c, "cs:precise/dummy-0", "cs:trusty/wordpress-42")
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"customized": {
			charm:  "cs:precise/dummy-0",
			config: charm.Settings{"username": "who", "skill-level": int64(47)},
		},
		"wordpress": {
			charm:  "cs:trusty/wordpress-42",
			config: charm.Settings{"blog-title": "these are the voyages"},
		},
	})
	s.assertUnitsCreated(c, map[string]string{
		"wordpress/0":  "1",
		"customized/0": "0",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleServiceUpgrade(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-42", "wordpress")
	testcharms.UploadCharm(c, s.client, "vivid/upgrade-1", "upgrade1")
	testcharms.UploadCharm(c, s.client, "vivid/upgrade-2", "upgrade2")

	// First deploy the bundle.
	output, err := s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: wordpress-42
                num_units: 1
                options:
                    blog-title: these are the voyages
            up:
                charm: vivid/upgrade-1
                num_units: 1
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:vivid/upgrade-1
service up deployed (charm: cs:vivid/upgrade-1)
added charm cs:trusty/wordpress-42
service wordpress deployed (charm: cs:trusty/wordpress-42)
service wordpress configured
added up/0 unit to new machine
added wordpress/0 unit to new machine
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertCharmsUplodaded(c, "cs:vivid/upgrade-1", "cs:trusty/wordpress-42")

	// Then deploy a new bundle with modified charm revision and options.
	output, err = s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: wordpress-42
                num_units: 1
                options:
                    blog-title: new title
            up:
                charm: vivid/upgrade-2
                num_units: 1
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput = `
added charm cs:vivid/upgrade-2
upgraded charm for existing service up (from cs:vivid/upgrade-1 to cs:vivid/upgrade-2)
added charm cs:trusty/wordpress-42
reusing service wordpress (charm: cs:trusty/wordpress-42)
service wordpress configured
avoid adding new units to service up: 1 unit already present
avoid adding new units to service wordpress: 1 unit already present
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertCharmsUplodaded(c, "cs:vivid/upgrade-1", "cs:vivid/upgrade-2", "cs:trusty/wordpress-42")
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"up": {charm: "cs:vivid/upgrade-2"},
		"wordpress": {
			charm:  "cs:trusty/wordpress-42",
			config: charm.Settings{"blog-title": "new title"},
		},
	})
	s.assertUnitsCreated(c, map[string]string{
		"up/0":        "0",
		"wordpress/0": "1",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleServiceUpgradeFailure(c *gc.C) {
	s.AddTestingService(c, "wordpress", s.AddTestingCharm(c, "wordpress"))

	// Try upgrading to a different charm name.
	testcharms.UploadCharm(c, s.client, "trusty/incompatible-42", "wordpress")
	_, err := s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: trusty/incompatible-42
                num_units: 1
    `)
	c.Assert(err, gc.ErrorMatches, `cannot deploy bundle: cannot upgrade service "wordpress": bundle charm "cs:trusty/incompatible-42" is incompatible with existing charm "local:quantal/wordpress-3"`)

	// Try upgrading to a different user.
	testcharms.UploadCharm(c, s.client, "~who/trusty/wordpress-42", "wordpress")
	_, err = s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: cs:~who/trusty/wordpress-42
                num_units: 1
    `)
	c.Assert(err, gc.ErrorMatches, `cannot deploy bundle: cannot upgrade service "wordpress": bundle charm "cs:~who/trusty/wordpress-42" is incompatible with existing charm "local:quantal/wordpress-3"`)

	// Try upgrading to a different series.
	testcharms.UploadCharm(c, s.client, "vivid/wordpress-42", "wordpress")
	_, err = s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: vivid/wordpress
                num_units: 1
    `)
	c.Assert(err, gc.ErrorMatches, `cannot deploy bundle: cannot upgrade service "wordpress": bundle charm "cs:vivid/wordpress-42" is incompatible with existing charm "local:quantal/wordpress-3"`)
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleMultipleRelations(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-0", "wordpress")
	testcharms.UploadCharm(c, s.client, "trusty/mysql-1", "mysql")
	testcharms.UploadCharm(c, s.client, "trusty/postgres-2", "mysql")
	testcharms.UploadCharm(c, s.client, "trusty/varnish-3", "varnish")
	output, err := s.deployBundleYAML(c, `
        services:
            wp:
                charm: wordpress
                num_units: 1
            mysql:
                charm: mysql-1
                num_units: 1
            pgres:
                charm: trusty/postgres-2
                num_units: 1
            varnish:
                charm: trusty/varnish
                num_units: 1
        relations:
            - ["wp:db", "mysql:server"]
            - ["wp:db", "pgres:server"]
            - ["varnish:webcache", "wp:cache"]
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/mysql-1
service mysql deployed (charm: cs:trusty/mysql-1)
added charm cs:trusty/postgres-2
service pgres deployed (charm: cs:trusty/postgres-2)
added charm cs:trusty/varnish-3
service varnish deployed (charm: cs:trusty/varnish-3)
added charm cs:trusty/wordpress-0
service wp deployed (charm: cs:trusty/wordpress-0)
related wp:db and mysql:server
related wp:db and pgres:server
related varnish:webcache and wp:cache
added mysql/0 unit to new machine
added pgres/0 unit to new machine
added varnish/0 unit to new machine
added wp/0 unit to new machine
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertRelationsEstablished(c, "wp:db mysql:server", "wp:db pgres:server", "wp:cache varnish:webcache")
	s.assertUnitsCreated(c, map[string]string{
		"mysql/0":   "0",
		"pgres/0":   "1",
		"varnish/0": "2",
		"wp/0":      "3",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleNewRelations(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-0", "wordpress")
	testcharms.UploadCharm(c, s.client, "trusty/mysql-1", "mysql")
	testcharms.UploadCharm(c, s.client, "trusty/postgres-2", "mysql")
	testcharms.UploadCharm(c, s.client, "trusty/varnish-3", "varnish")
	_, err := s.deployBundleYAML(c, `
        services:
            wp:
                charm: wordpress
                num_units: 1
            mysql:
                charm: mysql-1
                num_units: 1
            varnish:
                charm: trusty/varnish
                num_units: 1
        relations:
            - ["wp:db", "mysql:server"]
    `)
	c.Assert(err, jc.ErrorIsNil)
	output, err := s.deployBundleYAML(c, `
        services:
            wp:
                charm: wordpress
                num_units: 1
            mysql:
                charm: mysql-1
                num_units: 1
            varnish:
                charm: trusty/varnish
                num_units: 1
        relations:
            - ["wp:db", "mysql:server"]
            - ["varnish:webcache", "wp:cache"]
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/mysql-1
reusing service mysql (charm: cs:trusty/mysql-1)
added charm cs:trusty/varnish-3
reusing service varnish (charm: cs:trusty/varnish-3)
added charm cs:trusty/wordpress-0
reusing service wp (charm: cs:trusty/wordpress-0)
wp:db and mysql:server are already related
related varnish:webcache and wp:cache
avoid adding new units to service mysql: 1 unit already present
avoid adding new units to service varnish: 1 unit already present
avoid adding new units to service wp: 1 unit already present
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertRelationsEstablished(c, "wp:db mysql:server", "wp:cache varnish:webcache")
	s.assertUnitsCreated(c, map[string]string{
		"mysql/0":   "0",
		"varnish/0": "1",
		"wp/0":      "2",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleMachinesUnitsPlacement(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-0", "wordpress")
	testcharms.UploadCharm(c, s.client, "trusty/mysql-2", "mysql")
	content := `
        services:
            wp:
                charm: cs:trusty/wordpress-0
                num_units: 2
                to:
                    - 1
                    - lxc:2
                options:
                    blog-title: these are the voyages
            sql:
                charm: cs:trusty/mysql
                num_units: 2
                to:
                    - lxc:wp/0
                    - new
        machines:
            1:
                series: trusty
            2:
    `
	output, err := s.deployBundleYAML(c, content)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/mysql-2
service sql deployed (charm: cs:trusty/mysql-2)
added charm cs:trusty/wordpress-0
service wp deployed (charm: cs:trusty/wordpress-0)
service wp configured
created new machine 0 for holding wp unit
created new machine 1 for holding wp unit
added wp/0 unit to machine 0
created 0/lxc/0 container in machine 0 for holding sql unit
created new machine 2 for holding sql unit
created 1/lxc/0 container in machine 1 for holding wp unit
added sql/0 unit to machine 0/lxc/0
added sql/1 unit to machine 2
added wp/1 unit to machine 1/lxc/0
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"sql": {charm: "cs:trusty/mysql-2"},
		"wp": {
			charm:  "cs:trusty/wordpress-0",
			config: charm.Settings{"blog-title": "these are the voyages"},
		},
	})
	s.assertRelationsEstablished(c)
	s.assertUnitsCreated(c, map[string]string{
		"sql/0": "0/lxc/0",
		"sql/1": "2",
		"wp/0":  "0",
		"wp/1":  "1/lxc/0",
	})

	// Redeploy the same bundle again.
	output, err = s.deployBundleYAML(c, content)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput = `
added charm cs:trusty/mysql-2
reusing service sql (charm: cs:trusty/mysql-2)
added charm cs:trusty/wordpress-0
reusing service wp (charm: cs:trusty/wordpress-0)
service wp configured
avoid creating other machines to host wp units: 2 units already present
avoid adding new units to service wp: 2 units already present
avoid creating other machines to host sql units: 2 units already present
avoid adding new units to service sql: 2 units already present
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertUnitsCreated(c, map[string]string{
		"sql/0": "0/lxc/0",
		"sql/1": "2",
		"wp/0":  "0",
		"wp/1":  "1/lxc/0",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleMachineAttributes(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/django-42", "dummy")
	output, err := s.deployBundleYAML(c, `
        services:
            django:
                charm: cs:trusty/django-42
                num_units: 2
                to:
                    - 1
                    - new
        machines:
            1:
                series: trusty
                constraints: "cpu-cores=4 mem=4G"
                annotations:
                    foo: bar
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/django-42
service django deployed (charm: cs:trusty/django-42)
created new machine 0 for holding django unit
added django/0 unit to machine 0
created new machine 1 for holding django unit
added django/1 unit to machine 1
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertServicesDeployed(c, map[string]serviceInfo{
		"django": {charm: "cs:trusty/django-42"},
	})
	s.assertRelationsEstablished(c)
	s.assertUnitsCreated(c, map[string]string{
		"django/0": "0",
		"django/1": "1",
	})
	m, err := s.State.Machine("0")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(m.Series(), gc.Equals, "trusty")
	cons, err := m.Constraints()
	c.Assert(err, jc.ErrorIsNil)
	expectedCons, err := constraints.Parse("cpu-cores=4 mem=4G")
	c.Assert(err, jc.ErrorIsNil)
	c.Assert(cons, jc.DeepEquals, expectedCons)
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleTwiceScaleUp(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/django-42", "dummy")
	_, err := s.deployBundleYAML(c, `
        services:
            django:
                charm: cs:trusty/django-42
                num_units: 2
    `)
	c.Assert(err, jc.ErrorIsNil)
	output, err := s.deployBundleYAML(c, `
        services:
            django:
                charm: cs:trusty/django-42
                num_units: 5
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/django-42
reusing service django (charm: cs:trusty/django-42)
added django/2 unit to new machine
added django/3 unit to new machine
added django/4 unit to new machine
avoid adding new units to service django: 5 units already present
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertUnitsCreated(c, map[string]string{
		"django/0": "0",
		"django/1": "1",
		"django/2": "2",
		"django/3": "3",
		"django/4": "4",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleUnitPlacedInService(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/django-42", "dummy")
	testcharms.UploadCharm(c, s.client, "trusty/wordpress-0", "wordpress")
	output, err := s.deployBundleYAML(c, `
        services:
            wordpress:
                charm: wordpress
                num_units: 3
            django:
                charm: cs:trusty/django-42
                num_units: 2
                to: [wordpress]
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/django-42
service django deployed (charm: cs:trusty/django-42)
added charm cs:trusty/wordpress-0
service wordpress deployed (charm: cs:trusty/wordpress-0)
added wordpress/0 unit to new machine
added wordpress/1 unit to new machine
added wordpress/2 unit to new machine
added django/0 unit to machine 0
added django/1 unit to machine 1
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertUnitsCreated(c, map[string]string{
		"django/0":    "0",
		"django/1":    "1",
		"wordpress/0": "0",
		"wordpress/1": "1",
		"wordpress/2": "2",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleUnitColocationWithUnit(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/django-42", "dummy")
	testcharms.UploadCharm(c, s.client, "trusty/mem-47", "dummy")
	testcharms.UploadCharm(c, s.client, "trusty/rails-0", "dummy")
	output, err := s.deployBundleYAML(c, `
        services:
            memcached:
                charm: cs:trusty/mem-47
                num_units: 3
                to: [1, new]
            django:
                charm: cs:trusty/django-42
                num_units: 5
                to:
                    - memcached/0
                    - lxc:memcached/1
                    - lxc:memcached/2
                    - kvm:ror
            ror:
                charm: rails
                num_units: 2
                to:
                    - new
                    - 1
        machines:
            1:
                series: trusty
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/django-42
service django deployed (charm: cs:trusty/django-42)
added charm cs:trusty/mem-47
service memcached deployed (charm: cs:trusty/mem-47)
added charm cs:trusty/rails-0
service ror deployed (charm: cs:trusty/rails-0)
created new machine 0 for holding memcached unit
added memcached/0 unit to machine 0
added ror/0 unit to machine 0
created 0/kvm/0 container in machine 0 for holding django unit
created new machine 1 for holding memcached unit
created new machine 2 for holding memcached unit
created new machine 3 for holding ror unit
added django/0 unit to machine 0
added django/1 unit to machine 0/kvm/0
added memcached/1 unit to machine 1
added memcached/2 unit to machine 2
added ror/1 unit to machine 3
created 1/lxc/0 container in machine 1 for holding django unit
created 2/lxc/0 container in machine 2 for holding django unit
created 3/kvm/0 container in machine 3 for holding django unit
added django/2 unit to machine 1/lxc/0
added django/3 unit to machine 2/lxc/0
added django/4 unit to machine 3/kvm/0
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertUnitsCreated(c, map[string]string{
		"django/0":    "0",
		"django/1":    "0/kvm/0",
		"django/2":    "1/lxc/0",
		"django/3":    "2/lxc/0",
		"django/4":    "3/kvm/0",
		"memcached/0": "0",
		"memcached/1": "1",
		"memcached/2": "2",
		"ror/0":       "0",
		"ror/1":       "3",
	})
}

func (s *deployRepoCharmStoreSuite) TestDeployBundleUnitPlacedToMachines(c *gc.C) {
	testcharms.UploadCharm(c, s.client, "trusty/django-42", "dummy")
	output, err := s.deployBundleYAML(c, `
        services:
            django:
                charm: cs:django
                num_units: 7
                to:
                    - new
                    - 4
                    - kvm:8
                    - lxc:4
                    - lxc:4
                    - lxc:new
        machines:
            4:
            8:
    `)
	c.Assert(err, jc.ErrorIsNil)
	expectedOutput := `
added charm cs:trusty/django-42
service django deployed (charm: cs:trusty/django-42)
created new machine 0 for holding django unit
created new machine 1 for holding django unit
added django/0 unit to machine 0
created new machine 2 for holding django unit
created 1/kvm/0 container in machine 1 for holding django unit
created 0/lxc/0 container in machine 0 for holding django unit
created 0/lxc/1 container in machine 0 for holding django unit
created 3/lxc/0 container in new machine for holding django unit
created 4/lxc/0 container in new machine for holding django unit
added django/1 unit to machine 2
added django/2 unit to machine 1/kvm/0
added django/3 unit to machine 0/lxc/0
added django/4 unit to machine 0/lxc/1
added django/5 unit to machine 3/lxc/0
added django/6 unit to machine 4/lxc/0
deployment of bundle "local:bundle/example-0" completed`
	c.Assert(output, gc.Equals, strings.TrimSpace(expectedOutput))
	s.assertUnitsCreated(c, map[string]string{
		"django/0": "0",       // Machine "4" in the bundle.
		"django/1": "2",       // Machine "new" in the bundle.
		"django/2": "1/kvm/0", // The KVM container in bundle machine "8".
		"django/3": "0/lxc/0", // First LXC container in bundle machine "4".
		"django/4": "0/lxc/1", // Second LXC container in bundle machine "4".
		"django/5": "3/lxc/0", // First LXC in new machine.
		"django/6": "4/lxc/0", // Second LXC in new machine.
	})
}
