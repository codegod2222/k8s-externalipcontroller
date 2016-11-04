// Copyright 2016 Mirantis
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package ipmanager

import (
	"testing"
	"time"

	"github.com/Mirantis/k8s-externalipcontroller/pkg/workqueue"
	"github.com/coreos/etcd/client"
	"github.com/stretchr/testify/mock"
	"golang.org/x/net/context"
)

type setAction struct {
	key   string
	value string
	opts  *client.SetOptions
}

type testKeysApi struct {
	collection       map[string]*client.Node
	setActionTracker []setAction
	watcher          client.Watcher
}

type testWatcher struct {
	mock.Mock
}

func (w *testWatcher) Next(ctx context.Context) (*client.Response, error) {
	args := w.Called(ctx)
	return args.Get(0).(*client.Response), args.Error(1)
}

func NewTestKeysApi() *testKeysApi {
	return &testKeysApi{map[string]*client.Node{}, []setAction{}, &testWatcher{}}
}

func (k *testKeysApi) Get(ctx context.Context, key string, opts *client.GetOptions) (*client.Response, error) {
	if opts.Recursive {
		var nodes client.Nodes
		for _, node := range k.collection {
			nodes = append(nodes, node)
		}
		return &client.Response{Node: &client.Node{Nodes: nodes}}, nil
	}
	if _, ok := k.collection[key]; !ok {
		return nil, client.Error{Code: client.ErrorCodeKeyNotFound}
	}
	return &client.Response{Node: k.collection[key]}, nil
}

func (k *testKeysApi) Set(ctx context.Context, key, value string, opts *client.SetOptions) (*client.Response, error) {
	k.setActionTracker = append(k.setActionTracker, setAction{key, value, opts})
	if opts.PrevValue != "" && value != opts.PrevValue {
		return nil, client.Error{Code: client.ErrorCodeTestFailed}
	}
	if opts.PrevExist == client.PrevNoExist {
		if _, ok := k.collection[key]; ok {
			return nil, client.Error{Code: client.ErrorCodeTestFailed}
		}
	}
	k.collection[key] = &client.Node{Value: value, Key: key}
	return &client.Response{Node: k.collection[key]}, nil
}

func (k *testKeysApi) Delete(ctx context.Context, key string, opts *client.DeleteOptions) (*client.Response, error) {
	return nil, nil
}

func (k *testKeysApi) Create(ctx context.Context, key, value string) (*client.Response, error) {
	return k.Set(ctx, key, value, &client.SetOptions{PrevExist: client.PrevNoExist})
}

func (k *testKeysApi) CreateInOrder(ctx context.Context, dir, value string, opts *client.CreateInOrderOptions) (*client.Response, error) {
	return nil, nil
}

func (k *testKeysApi) Update(ctx context.Context, key, value string) (*client.Response, error) {
	return k.Set(ctx, key, value, &client.SetOptions{PrevExist: client.PrevExist})
}

func (k *testKeysApi) Watcher(key string, opts *client.WatcherOptions) client.Watcher {
	return k.watcher
}

func failIfErr(t *testing.T, err error) {
	if err != nil {
		t.Errorf("Unexcpeted error %v", err)
	}
}

func TestFairManager(t *testing.T) {
	client := NewTestKeysApi()
	stop := make(chan struct{})
	fair := &FairEtcd{client: client, stop: stop, opts: defaultOpts}
	fit, err := fair.Fit("1", "10.10.0.2/24")
	failIfErr(t, err)
	if !fit {
		t.Errorf("Expected to fit on Uid 1 %v", client.collection)
	}
	fit, err = fair.Fit("2", "10.10.0.3/24")
	failIfErr(t, err)
	if !fit {
		t.Errorf("Expected to fit on Uid 2 %v", client.collection)
	}
	fit, err = fair.Fit("2", "10.10.0.4/24")
	failIfErr(t, err)
	if !fit {
		t.Errorf("Expected to fit on Uid 2 %v", client.collection)
	}
	fit, err = fair.Fit("2", "10.10.0.5/24")
	failIfErr(t, err)
	if fit {
		t.Errorf("Expected not to fit on Uid 2 %v", client.collection)
	}
	fit, err = fair.Fit("1", "10.10.0.5/24")
	failIfErr(t, err)
	if !fit {
		t.Errorf("Expected not to fit on Uid 1 %v", client.collection)
	}
	close(stop)
}

func TestTtlRenew(t *testing.T) {
	uid := "1"
	cidr := "10.10.0.2/24"
	kclient := NewTestKeysApi()
	stop := make(chan struct{})
	opts := FairEtcdOpts{ttlDuration: 1 * time.Second, ttlRenewInterval: 100 * time.Millisecond}
	fair := &FairEtcd{client: kclient, stop: stop, ttlInitialized: map[string]bool{}, opts: opts}
	fair.Fit(uid, cidr)
	time.Sleep(300 * time.Millisecond)
	close(stop)
	actions := kclient.setActionTracker[1:]

	if len(actions) < 2 {
		t.Errorf("Expected to see alteast 2 calls. 1 prevExist=false, all others prevValue=%v. %v", cidr, kclient.setActionTracker)
	}
	for i, act := range actions {
		if i == 0 && act.opts.PrevExist != client.PrevNoExist {
			t.Errorf("1st call should have PrevExist=true, %v", act)
		}
		if i > 0 && act.opts.PrevValue != uid {
			t.Errorf("all calls after 1st must have PrevValue=%v, %v", uid, act)
		}
		if act.opts.TTL != opts.ttlDuration {
			t.Errorf("ttl duration must be configured to one which is provided in opts %v, %v", opts.ttlDuration, act)
		}
	}
}

func TestExpireWatcher(t *testing.T) {
	watcher := &testWatcher{}
	kclient := NewTestKeysApi()
	kclient.watcher = watcher
	stop := make(chan struct{})
	opts := FairEtcdOpts{ttlDuration: 1 * time.Second, ttlRenewInterval: 100 * time.Millisecond}

	var times int

	fair := &FairEtcd{
		client:         kclient,
		stop:           stop,
		ttlInitialized: map[string]bool{},
		opts:           opts,
		queue:          workqueue.NewQueue()}
	watcher.On("Next", context.Background()).Return(
		&client.Response{Action: "expire", Node: &client.Node{Key: "/collection/10.10.0.2::24"}},
		nil,
	).Times(3).Run(func(_ mock.Arguments) {
		times++
		if times == 3 {
			close(stop)
		}
	})

	fair.loopWatchExpired(stop)

	if fair.queue.Len() != 3 {
		t.Errorf("Expected to see 3 items added to a queue, instead we see %d", fair.queue.Len())
	}
}

func TestCidrToKey(t *testing.T) {
	testCases := []struct {
		prefix string
		cidr   string
		key    string
	}{
		{
			prefix: "/ips/",
			cidr:   "10.10.0.2/24",
			key:    "/ips/10.10.0.2::24",
		},
		{
			prefix: "/ips/",
			cidr:   "1",
			key:    "/ips/1",
		},
	}

	for _, test := range testCases {
		if val := keyFromCidr(test.prefix, test.cidr); val != test.key {
			t.Errorf("keyFromCidr returned incorrect result: %s != %s", val, test.key)
		}
		if val := cidrFromKey(test.key); val != test.cidr {
			t.Errorf("cidrFromKey returned incorrect result: %s != %s", val, test.cidr)
		}
	}
}
