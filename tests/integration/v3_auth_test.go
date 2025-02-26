// Copyright 2017 The etcd Authors
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

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/authpb"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	"go.etcd.io/etcd/client/pkg/v3/testutil"
	clientv3 "go.etcd.io/etcd/client/v3"
	"go.etcd.io/etcd/tests/v3/framework/integration"
)

// TestV3AuthEmptyUserGet ensures that a get with an empty user will return an empty user error.
func TestV3AuthEmptyUserGet(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	defer cancel()

	api := integration.ToGRPC(clus.Client(0))
	authSetupRoot(t, api.Auth)

	_, err := api.KV.Range(ctx, &pb.RangeRequest{Key: []byte("abc")})
	if !eqErrGRPC(err, rpctypes.ErrUserEmpty) {
		t.Fatalf("got %v, expected %v", err, rpctypes.ErrUserEmpty)
	}
}

// TestV3AuthEmptyUserPut ensures that a put with an empty user will return an empty user error,
// and the consistent_index should be moved forward even the apply-->Put fails.
func TestV3AuthEmptyUserPut(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{
		Size:          1,
		SnapshotCount: 3,
	})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(context.TODO(), 30*time.Second)
	defer cancel()

	api := integration.ToGRPC(clus.Client(0))
	authSetupRoot(t, api.Auth)

	// The SnapshotCount is 3, so there must be at least 3 new snapshot files being created.
	// The VERIFY logic will check whether the consistent_index >= last snapshot index on
	// cluster terminating.
	for i := 0; i < 10; i++ {
		_, err := api.KV.Put(ctx, &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
		if !eqErrGRPC(err, rpctypes.ErrUserEmpty) {
			t.Fatalf("got %v, expected %v", err, rpctypes.ErrUserEmpty)
		}
	}
}

// TestV3AuthTokenWithDisable tests that auth won't crash if
// given a valid token when authentication is disabled
func TestV3AuthTokenWithDisable(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "root", Password: "123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer c.Close()

	rctx, cancel := context.WithCancel(context.TODO())
	donec := make(chan struct{})
	go func() {
		defer close(donec)
		for rctx.Err() == nil {
			c.Put(rctx, "abc", "def")
		}
	}()

	time.Sleep(10 * time.Millisecond)
	if _, err := c.AuthDisable(context.TODO()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	cancel()
	<-donec
}

func TestV3AuthRevision(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	api := integration.ToGRPC(clus.Client(0))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	presp, perr := api.KV.Put(ctx, &pb.PutRequest{Key: []byte("foo"), Value: []byte("bar")})
	cancel()
	if perr != nil {
		t.Fatal(perr)
	}
	rev := presp.Header.Revision

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	aresp, aerr := api.Auth.UserAdd(ctx, &pb.AuthUserAddRequest{Name: "root", Password: "123", Options: &authpb.UserAddOptions{NoPassword: false}})
	cancel()
	if aerr != nil {
		t.Fatal(aerr)
	}
	if aresp.Header.Revision != rev {
		t.Fatalf("revision expected %d, got %d", rev, aresp.Header.Revision)
	}
}

// TestV3AuthWithLeaseRevokeWithRoot ensures that granted leases
// with root user be revoked after TTL.
func TestV3AuthWithLeaseRevokeWithRoot(t *testing.T) {
	testV3AuthWithLeaseRevokeWithRoot(t, integration.ClusterConfig{Size: 1})
}

// TestV3AuthWithLeaseRevokeWithRootJWT creates a lease with a JWT-token enabled cluster.
// And tests if server is able to revoke expiry lease item.
func TestV3AuthWithLeaseRevokeWithRootJWT(t *testing.T) {
	testV3AuthWithLeaseRevokeWithRoot(t, integration.ClusterConfig{Size: 1, AuthToken: integration.DefaultTokenJWT})
}

func testV3AuthWithLeaseRevokeWithRoot(t *testing.T, ccfg integration.ClusterConfig) {
	integration.BeforeTest(t)

	clus := integration.NewCluster(t, &ccfg)
	defer clus.Terminate(t)

	api := integration.ToGRPC(clus.Client(0))
	authSetupRoot(t, api.Auth)

	rootc, cerr := integration.NewClient(t, clientv3.Config{
		Endpoints: clus.Client(0).Endpoints(),
		Username:  "root",
		Password:  "123",
	})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer rootc.Close()

	leaseResp, err := rootc.Grant(context.TODO(), 2)
	if err != nil {
		t.Fatal(err)
	}
	leaseID := leaseResp.ID

	if _, err = rootc.Put(context.TODO(), "foo", "bar", clientv3.WithLease(leaseID)); err != nil {
		t.Fatal(err)
	}

	// wait for lease expire
	time.Sleep(3 * time.Second)

	tresp, terr := rootc.TimeToLive(
		context.TODO(),
		leaseID,
		clientv3.WithAttachedKeys(),
	)
	if terr != nil {
		t.Error(terr)
	}
	if len(tresp.Keys) > 0 || tresp.GrantedTTL != 0 {
		t.Errorf("lease %016x should have been revoked, got %+v", leaseID, tresp)
	}
	if tresp.TTL != -1 {
		t.Errorf("lease %016x should have been expired, got %+v", leaseID, tresp)
	}
}

type user struct {
	name     string
	password string
	role     string
	key      string
	end      string
}

func TestV3AuthWithLeaseRevoke(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			key:      "k1",
			end:      "k2",
		},
	}
	authSetupUsers(t, integration.ToGRPC(clus.Client(0)).Auth, users)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	rootc, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "root", Password: "123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer rootc.Close()

	leaseResp, err := rootc.Grant(context.TODO(), 90)
	if err != nil {
		t.Fatal(err)
	}
	leaseID := leaseResp.ID
	// permission of k3 isn't granted to user1
	_, err = rootc.Put(context.TODO(), "k3", "val", clientv3.WithLease(leaseID))
	if err != nil {
		t.Fatal(err)
	}

	userc, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user1", Password: "user1-123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer userc.Close()
	_, err = userc.Revoke(context.TODO(), leaseID)
	if err == nil {
		t.Fatal("revoking from user1 should be failed with permission denied")
	}
}

func TestV3AuthWithLeaseAttach(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			key:      "k1",
			end:      "k3",
		},
		{
			name:     "user2",
			password: "user2-123",
			role:     "role2",
			key:      "k2",
			end:      "k4",
		},
	}
	authSetupUsers(t, integration.ToGRPC(clus.Client(0)).Auth, users)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	user1c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user1", Password: "user1-123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer user1c.Close()

	user2c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user2", Password: "user2-123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer user2c.Close()

	leaseResp, err := user1c.Grant(context.TODO(), 90)
	if err != nil {
		t.Fatal(err)
	}
	leaseID := leaseResp.ID
	// permission of k2 is also granted to user2
	_, err = user1c.Put(context.TODO(), "k2", "val", clientv3.WithLease(leaseID))
	if err != nil {
		t.Fatal(err)
	}

	_, err = user2c.Revoke(context.TODO(), leaseID)
	if err != nil {
		t.Fatal(err)
	}

	leaseResp, err = user1c.Grant(context.TODO(), 90)
	if err != nil {
		t.Fatal(err)
	}
	leaseID = leaseResp.ID
	// permission of k1 isn't granted to user2
	_, err = user1c.Put(context.TODO(), "k1", "val", clientv3.WithLease(leaseID))
	if err != nil {
		t.Fatal(err)
	}

	_, err = user2c.Revoke(context.TODO(), leaseID)
	if err == nil {
		t.Fatal("revoking from user2 should be failed with permission denied")
	}
}

func authSetupUsers(t *testing.T, auth pb.AuthClient, users []user) {
	for _, user := range users {
		if _, err := auth.UserAdd(context.TODO(), &pb.AuthUserAddRequest{Name: user.name, Password: user.password, Options: &authpb.UserAddOptions{NoPassword: false}}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.RoleAdd(context.TODO(), &pb.AuthRoleAddRequest{Name: user.role}); err != nil {
			t.Fatal(err)
		}
		if _, err := auth.UserGrantRole(context.TODO(), &pb.AuthUserGrantRoleRequest{User: user.name, Role: user.role}); err != nil {
			t.Fatal(err)
		}

		if len(user.key) == 0 {
			continue
		}

		perm := &authpb.Permission{
			PermType: authpb.READWRITE,
			Key:      []byte(user.key),
			RangeEnd: []byte(user.end),
		}
		if _, err := auth.RoleGrantPermission(context.TODO(), &pb.AuthRoleGrantPermissionRequest{Name: user.role, Perm: perm}); err != nil {
			t.Fatal(err)
		}
	}
}

func authSetupRoot(t *testing.T, auth pb.AuthClient) {
	root := []user{
		{
			name:     "root",
			password: "123",
			role:     "root",
			key:      "",
		},
	}
	authSetupUsers(t, auth, root)
	if _, err := auth.AuthEnable(context.TODO(), &pb.AuthEnableRequest{}); err != nil {
		t.Fatal(err)
	}
}

func TestV3AuthNonAuthorizedRPCs(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	nonAuthedKV := clus.Client(0).KV

	key := "foo"
	val := "bar"
	_, err := nonAuthedKV.Put(context.TODO(), key, val)
	if err != nil {
		t.Fatalf("couldn't put key (%v)", err)
	}

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	respput, err := nonAuthedKV.Put(context.TODO(), key, val)
	if !eqErrGRPC(err, rpctypes.ErrGRPCUserEmpty) {
		t.Fatalf("could put key (%v), it should cause an error of permission denied", respput)
	}
}

func TestV3AuthOldRevConcurrent(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	c, cerr := integration.NewClient(t, clientv3.Config{
		Endpoints:   clus.Client(0).Endpoints(),
		DialTimeout: 5 * time.Second,
		Username:    "root",
		Password:    "123",
	})
	testutil.AssertNil(t, cerr)
	defer c.Close()

	var wg sync.WaitGroup
	f := func(i int) {
		defer wg.Done()
		role, user := fmt.Sprintf("test-role-%d", i), fmt.Sprintf("test-user-%d", i)
		_, err := c.RoleAdd(context.TODO(), role)
		testutil.AssertNil(t, err)
		_, err = c.RoleGrantPermission(context.TODO(), role, "\x00", clientv3.GetPrefixRangeEnd(""), clientv3.PermissionType(clientv3.PermReadWrite))
		testutil.AssertNil(t, err)
		_, err = c.UserAdd(context.TODO(), user, "123")
		testutil.AssertNil(t, err)
		_, err = c.Put(context.TODO(), "a", "b")
		testutil.AssertNil(t, err)
	}
	// needs concurrency to trigger
	numRoles := 2
	wg.Add(numRoles)
	for i := 0; i < numRoles; i++ {
		go f(i)
	}
	wg.Wait()
}

func TestV3AuthRestartMember(t *testing.T) {
	integration.BeforeTest(t)

	// create a cluster with 1 member
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	// create a client
	c, cerr := integration.NewClient(t, clientv3.Config{
		Endpoints:   clus.Client(0).Endpoints(),
		DialTimeout: 5 * time.Second,
	})
	testutil.AssertNil(t, cerr)
	defer c.Close()

	authData := []struct {
		user string
		role string
		pass string
	}{
		{
			user: "root",
			role: "root",
			pass: "123",
		},
		{
			user: "user0",
			role: "role0",
			pass: "123",
		},
	}

	for _, authObj := range authData {
		// add a role
		_, err := c.RoleAdd(context.TODO(), authObj.role)
		testutil.AssertNil(t, err)
		// add a user
		_, err = c.UserAdd(context.TODO(), authObj.user, authObj.pass)
		testutil.AssertNil(t, err)
		// grant role to user
		_, err = c.UserGrantRole(context.TODO(), authObj.user, authObj.role)
		testutil.AssertNil(t, err)
	}

	// role grant permission to role0
	_, err := c.RoleGrantPermission(context.TODO(), authData[1].role, "foo", "", clientv3.PermissionType(clientv3.PermReadWrite))
	testutil.AssertNil(t, err)

	// enable auth
	_, err = c.AuthEnable(context.TODO())
	testutil.AssertNil(t, err)

	// create another client with ID:Password
	c2, cerr := integration.NewClient(t, clientv3.Config{
		Endpoints:   clus.Client(0).Endpoints(),
		DialTimeout: 5 * time.Second,
		Username:    authData[1].user,
		Password:    authData[1].pass,
	})
	testutil.AssertNil(t, cerr)
	defer c2.Close()

	// create foo since that is within the permission set
	// expectation is to succeed
	_, err = c2.Put(context.TODO(), "foo", "bar")
	testutil.AssertNil(t, err)

	clus.Members[0].Stop(t)
	err = clus.Members[0].Restart(t)
	testutil.AssertNil(t, err)
	integration.WaitClientV3WithKey(t, c2.KV, "foo")

	// nothing has changed, but it fails without refreshing cache after restart
	_, err = c2.Put(context.TODO(), "foo", "bar2")
	testutil.AssertNil(t, err)
}

func TestV3AuthWatchAndTokenExpire(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1, AuthTokenTTL: 3})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	defer cancel()

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "root", Password: "123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer c.Close()

	_, err := c.Put(ctx, "key", "val")
	if err != nil {
		t.Fatalf("Unexpected error from Put: %v", err)
	}

	// The first watch gets a valid auth token through watcher.newWatcherGrpcStream()
	// We should discard the first one by waiting TTL after the first watch.
	wChan := c.Watch(ctx, "key", clientv3.WithRev(1))
	watchResponse := <-wChan

	time.Sleep(5 * time.Second)

	wChan = c.Watch(ctx, "key", clientv3.WithRev(1))
	watchResponse = <-wChan
	testutil.AssertNil(t, watchResponse.Err())
}

func TestV3AuthWatchErrorAndWatchId0(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
	defer cancel()

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			key:      "k1",
			end:      "k2",
		},
	}
	authSetupUsers(t, integration.ToGRPC(clus.Client(0)).Auth, users)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user1", Password: "user1-123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer c.Close()

	watchStartCh, watchEndCh := make(chan interface{}), make(chan interface{})

	go func() {
		wChan := c.Watch(ctx, "k1", clientv3.WithRev(1))
		watchStartCh <- struct{}{}
		watchResponse := <-wChan
		t.Logf("watch response from k1: %v", watchResponse)
		testutil.AssertTrue(t, len(watchResponse.Events) != 0)
		watchEndCh <- struct{}{}
	}()

	// Chan for making sure that the above goroutine invokes Watch()
	// So the above Watch() can get watch ID = 0
	<-watchStartCh

	wChan := c.Watch(ctx, "non-allowed-key", clientv3.WithRev(1))
	watchResponse := <-wChan
	testutil.AssertNotNil(t, watchResponse.Err()) // permission denied

	_, err := c.Put(ctx, "k1", "val")
	if err != nil {
		t.Fatalf("Unexpected error from Put: %v", err)
	}

	<-watchEndCh
}

func TestV3AuthWithLeaseTimeToLive(t *testing.T) {
	integration.BeforeTest(t)
	clus := integration.NewCluster(t, &integration.ClusterConfig{Size: 1})
	defer clus.Terminate(t)

	users := []user{
		{
			name:     "user1",
			password: "user1-123",
			role:     "role1",
			key:      "k1",
			end:      "k3",
		},
		{
			name:     "user2",
			password: "user2-123",
			role:     "role2",
			key:      "k2",
			end:      "k4",
		},
	}
	authSetupUsers(t, integration.ToGRPC(clus.Client(0)).Auth, users)

	authSetupRoot(t, integration.ToGRPC(clus.Client(0)).Auth)

	user1c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user1", Password: "user1-123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer user1c.Close()

	user2c, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "user2", Password: "user2-123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer user2c.Close()

	leaseResp, err := user1c.Grant(context.TODO(), 90)
	if err != nil {
		t.Fatal(err)
	}
	leaseID := leaseResp.ID
	_, err = user1c.Put(context.TODO(), "k1", "val", clientv3.WithLease(leaseID))
	if err != nil {
		t.Fatal(err)
	}
	// k2 can be accessed from both user1 and user2
	_, err = user1c.Put(context.TODO(), "k2", "val", clientv3.WithLease(leaseID))
	if err != nil {
		t.Fatal(err)
	}

	_, err = user1c.TimeToLive(context.TODO(), leaseID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = user2c.TimeToLive(context.TODO(), leaseID)
	if err != nil {
		t.Fatal(err)
	}

	_, err = user2c.TimeToLive(context.TODO(), leaseID, clientv3.WithAttachedKeys())
	if err == nil {
		t.Fatal("timetolive from user2 should be failed with permission denied")
	}

	rootc, cerr := integration.NewClient(t, clientv3.Config{Endpoints: clus.Client(0).Endpoints(), Username: "root", Password: "123"})
	if cerr != nil {
		t.Fatal(cerr)
	}
	defer rootc.Close()

	if _, err := rootc.RoleRevokePermission(context.TODO(), "role1", "k1", "k3"); err != nil {
		t.Fatal(err)
	}

	_, err = user1c.TimeToLive(context.TODO(), leaseID, clientv3.WithAttachedKeys())
	if err == nil {
		t.Fatal("timetolive from user2 should be failed with permission denied")
	}
}
