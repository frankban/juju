// Copyright 2016 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package gui

import (
	"fmt"
	"sort"

	"github.com/juju/errors"
	"github.com/juju/utils"
	"github.com/juju/version"

	"github.com/juju/juju/environs/simplestreams"
	"github.com/juju/juju/tools"
)

const (
	baseURL        = "https://temp-streams.s3.amazonaws.com/gui-unsigned"
	develStream    = "devel" // "released"
	streamsVersion = "v1"
	downloadType   = "content-download"
)

func init() {
	simplestreams.RegisterStructTags(metadata{})
}

// FetchGUIArchives fetches all Juju GUI metadata from simplestreams and
// returns a list of corresponding GUI archives, sorted by version descending.
func FetchGUIArchives() ([]*tools.GUIArchive, error) {
	source := simplestreams.NewURLDataSource(
		"default simplestreams", baseURL, utils.NoVerifySSLHostnames, simplestreams.DEFAULT_CLOUD_DATA, false)
	allMeta, err := fetch([]simplestreams.DataSource{source}, develStream)
	if err != nil {
		return nil, errors.Annotate(err, "error fetching simplestreams metadata")
	}
	guiArchives := make([]*tools.GUIArchive, len(allMeta))
	for i, meta := range allMeta {
		vers, err := version.Parse(meta.Version)
		if err != nil {
			return nil, errors.Annotatef(err, "error parsing version %q", meta.Version)
		}
		guiArchives[i] = &tools.GUIArchive{
			Version: vers,
			URL:     meta.FullPath,
			Size:    meta.Size,
			SHA256:  meta.SHA256,
		}
	}
	sort.Sort(byVersion(guiArchives))
	return guiArchives, nil
}

// byVersion is used to sort GUI archives by version, most recent first.
type byVersion []*tools.GUIArchive

func (b byVersion) Len() int           { return len(b) }
func (b byVersion) Swap(i, j int)      { b[i], b[j] = b[j], b[i] }
func (b byVersion) Less(i, j int) bool { return b[i].Version.Compare(b[j].Version) > 0 }

// fetch fetches Juju GUI metadata from simplestreams.
func fetch(sources []simplestreams.DataSource, stream string) ([]*metadata, error) {
	params := simplestreams.GetMetadataParams{
		StreamsVersion: streamsVersion,
		LookupConstraint: &constraint{
			LookupParams: simplestreams.LookupParams{Stream: stream},
		},
		ValueParams: simplestreams.ValueParams{
			DataType:        downloadType,
			MirrorContentId: contentId(stream),
			FilterFunc:      appendArchives,
			ValueTemplate:   metadata{},
		},
	}
	items, _, err := simplestreams.GetMetadata(sources, params)
	if err != nil {
		return nil, err
	}
	allMeta := make([]*metadata, len(items))
	for i, item := range items {
		allMeta[i] = item.(*metadata)
	}
	return allMeta, nil
}

// constraint is used as simplestreams.LookupConstraint when retrieving Juju
// GUI metadata information/
type constraint struct {
	simplestreams.LookupParams
}

// IndexIds generates a string array representing index ids formed similarly to
// an ISCSI qualified name (IQN).
func (c *constraint) IndexIds() []string {
	return []string{contentId(c.Stream)}
}

// ProductIds generates a string array representing product ids formed
// similarly to an ISCSI qualified name (IQN).
func (c *constraint) ProductIds() ([]string, error) {
	return []string{"com.canonical.streams:gui"}, nil
}

// contentId returns the GUI content id in simplestreams for the given stream.
func contentId(stream string) string {
	return fmt.Sprintf("com.canonical.streams:%s:gui", stream)
}

// appendArchives collects all matching Juju GUi archive metadata information.
func appendArchives(
	source simplestreams.DataSource,
	matchingItems []interface{},
	items map[string]interface{},
	cons simplestreams.LookupConstraint,
) ([]interface{}, error) {
	for _, item := range items {
		meta := item.(*metadata)
		fullPath, err := source.URL(meta.Path)
		if err != nil {
			return nil, err
		}
		meta.FullPath = fullPath
		matchingItems = append(matchingItems, meta)
	}
	return matchingItems, nil
}

// metadata is the type used to retrieve GUI archive metadata information from
// simplestream. Tags for this structure are registered in init().
type metadata struct {
	Version string `json:"version"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
	Path    string `json:"path"`

	FullPath string `json:"-"`
}
