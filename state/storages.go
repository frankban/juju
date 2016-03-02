// Copyright 2014 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package state

import (
	jujutxn "github.com/juju/txn"
	"gopkg.in/juju/blobstore.v2"
	"gopkg.in/mgo.v2"

	"github.com/juju/juju/state/binarystorage"
)

var binarystorageNew = binarystorage.New

// ToolsStorage returns a new binarystorage.StorageCloser that stores tools
// metadata in the "juju" database "toolsmetadata" collection.
//
// TODO(axw) remove this, add a constructor function in binarystorage.
func (st *State) ToolsStorage() (binarystorage.StorageCloser, error) {
	return newStorage(st, db.C(toolsmetadataC)), nil
}

// GUIStorage returns a new binarystorage.StorageCloser that stores GUI archive
// metadata in the "juju" database "guimetadata" collection.
func (st *State) GUIStorage() (binarystorage.StorageCloser, error) {
	return newStorage(st, db.C(guimetadataC)), nil
}

func newStorage(st *State, metadataCollection *mgo.Collection) binarystorage.StorageCloser {
	uuid := st.ModelUUID()
	session := st.session.Copy()
	rs := blobstore.NewGridFS(blobstoreDB, uuid, session)
	db := session.DB(jujuDB)
	txnRunner := jujutxn.NewRunner(jujutxn.RunnerParams{Database: db})
	managedStorage := blobstore.NewManagedStorage(db, rs)
	storage := binarystorageNew(uuid, managedStorage, metadataCollection, txnRunner)
	return &storageCloser{storage, session}
}

type storageCloser struct {
	binarystorage.Storage
	session *mgo.Session
}

func (t *storageCloser) Close() error {
	t.session.Close()
	return nil
}
