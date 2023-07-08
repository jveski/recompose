package main

import "sync"

type nodeMetadataStore struct {
	lock          sync.Mutex
	byFingerprint map[string]*nodeMetadata
}

func newNodeMetadataStore() *nodeMetadataStore {
	return &nodeMetadataStore{byFingerprint: make(map[string]*nodeMetadata)}
}

func (n *nodeMetadataStore) Set(fingerprint string, meta *nodeMetadata) {
	n.lock.Lock()
	defer n.lock.Unlock()
	n.byFingerprint[fingerprint] = meta
}

func (n *nodeMetadataStore) Get(fingerprint string) *nodeMetadata {
	n.lock.Lock()
	defer n.lock.Unlock()
	return n.byFingerprint[fingerprint]
}

func (n *nodeMetadataStore) List() []*nodeMetadata {
	n.lock.Lock()
	defer n.lock.Unlock()

	slice := []*nodeMetadata{}
	for _, node := range n.byFingerprint {
		slice = append(slice, node)
	}
	return slice
}

type nodeMetadata struct {
	Fingerprint string
	IP          string
	APIPort     uint
}
