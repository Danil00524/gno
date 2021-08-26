package sdk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/gnolang/gno/pkgs/amino"
	abci "github.com/gnolang/gno/pkgs/bft/abci/types"
	bft "github.com/gnolang/gno/pkgs/bft/types"
	"github.com/gnolang/gno/pkgs/crypto"
	dbm "github.com/gnolang/gno/pkgs/db"
	"github.com/gnolang/gno/pkgs/errors"
	"github.com/gnolang/gno/pkgs/log"
	"github.com/gnolang/gno/pkgs/std"
	"github.com/gnolang/gno/pkgs/store/iavl"
	store "github.com/gnolang/gno/pkgs/store/types"
)

var (
	capKey1 = store.NewStoreKey("key1")
	capKey2 = store.NewStoreKey("key2")
)

func defaultLogger() log.Logger {
	return log.NewTMLogger(log.NewSyncWriter(os.Stdout)).With("module", "sdk/app")
}

func newBaseApp(name string, options ...func(*BaseApp)) *BaseApp {
	logger := defaultLogger()
	db := dbm.NewMemDB()
	return NewBaseApp(name, logger, db, options...)
}

// simple one store baseapp
func setupBaseApp(t *testing.T, options ...func(*BaseApp)) *BaseApp {
	app := newBaseApp(t.Name(), options...)
	require.Equal(t, t.Name(), app.Name())

	// no stores are mounted
	require.Panics(t, func() {
		app.LoadLatestVersion(capKey1)
	})

	app.MountStoreWithDB(capKey1, iavl.StoreConstructor, nil)
	app.MountStoreWithDB(capKey2, iavl.StoreConstructor, nil)

	// stores are mounted
	err := app.LoadLatestVersion(capKey1)
	require.Nil(t, err)
	return app
}

func TestMountStores(t *testing.T) {
	app := setupBaseApp(t)

	// check both stores
	store1 := app.cms.GetCommitStore(capKey1)
	require.NotNil(t, store1)
	store2 := app.cms.GetCommitStore(capKey2)
	require.NotNil(t, store2)
}

// Test that we can make commits and then reload old versions.
// Test that LoadLatestVersion actually does.
func TestLoadVersion(t *testing.T) {
	logger := defaultLogger()
	pruningOpt := SetPruningOptions(store.PruneSyncable)
	db := dbm.NewMemDB()
	name := t.Name()
	app := NewBaseApp(name, logger, db, nil, pruningOpt)

	// make a cap key and mount the store
	capKey := store.NewStoreKey(MainStoreKey)
	app.MountStoreWithDB(capKey, iavl.StoreConstructor, nil)
	err := app.LoadLatestVersion(capKey) // needed to make stores non-nil
	require.Nil(t, err)

	emptyCommitID := store.CommitID{}

	// fresh store has zero/empty last commit
	lastHeight := app.LastBlockHeight()
	lastID := app.LastCommitID()
	require.Equal(t, int64(0), lastHeight)
	require.Equal(t, emptyCommitID, lastID)

	// execute a block, collect commit ID
	header := &bft.Header{Height: 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})
	res := app.Commit()
	commitID1 := store.CommitID{1, res.Data}

	// execute a block, collect commit ID
	header = &bft.Header{Height: 2}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})
	res = app.Commit()
	commitID2 := store.CommitID{2, res.Data}

	// reload with LoadLatestVersion
	app = NewBaseApp(name, logger, db, nil, pruningOpt)
	app.MountStoreWithDB(capKey, iavl.StoreConstructor, nil)
	err = app.LoadLatestVersion(capKey)
	require.Nil(t, err)
	testLoadVersionHelper(t, app, int64(2), commitID2)

	// reload with LoadVersion, see if you can commit the same block and get
	// the same result
	app = NewBaseApp(name, logger, db, nil, pruningOpt)
	app.MountStoreWithDB(capKey, iavl.StoreConstructor, nil)
	err = app.LoadVersion(1, capKey)
	require.Nil(t, err)
	testLoadVersionHelper(t, app, int64(1), commitID1)
	app.BeginBlock(abci.RequestBeginBlock{Header: header})
	app.Commit()
	testLoadVersionHelper(t, app, int64(2), commitID2)
}

func TestAppVersionSetterGetter(t *testing.T) {
	logger := defaultLogger()
	pruningOpt := SetPruningOptions(store.PruneSyncable)
	db := dbm.NewMemDB()
	name := t.Name()
	app := NewBaseApp(name, logger, db, nil, pruningOpt)

	require.Equal(t, "", app.AppVersion())
	res := app.Query(abci.RequestQuery{Path: "app/version"})
	require.True(t, res.IsOK())
	require.Equal(t, "", string(res.Value))

	versionString := "1.0.0"
	app.SetAppVersion(versionString)
	require.Equal(t, versionString, app.AppVersion())
	res = app.Query(abci.RequestQuery{Path: "app/version"})
	require.True(t, res.IsOK())
	require.Equal(t, versionString, string(res.Value))
}

func TestLoadVersionInvalid(t *testing.T) {
	logger := log.NewNopLogger()
	pruningOpt := SetPruningOptions(store.PruneSyncable)
	db := dbm.NewMemDB()
	name := t.Name()
	app := NewBaseApp(name, logger, db, nil, pruningOpt)

	capKey := store.NewStoreKey(MainStoreKey)
	app.MountStoreWithDB(capKey, iavl.StoreConstructor, nil)
	err := app.LoadLatestVersion(capKey)
	require.Nil(t, err)

	// require error when loading an invalid version
	err = app.LoadVersion(-1, capKey)
	require.Error(t, err)

	header := &bft.Header{Height: 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})
	res := app.Commit()
	commitID1 := store.CommitID{1, res.Data}

	// create a new app with the stores mounted under the same cap key
	app = NewBaseApp(name, logger, db, nil, pruningOpt)
	app.MountStoreWithDB(capKey, iavl.StoreConstructor, nil)

	// require we can load the latest version
	err = app.LoadVersion(1, capKey)
	require.Nil(t, err)
	testLoadVersionHelper(t, app, int64(1), commitID1)

	// require error when loading an invalid version
	err = app.LoadVersion(2, capKey)
	require.Error(t, err)
}

func testLoadVersionHelper(t *testing.T, app *BaseApp, expectedHeight int64, expectedID store.CommitID) {
	lastHeight := app.LastBlockHeight()
	lastID := app.LastCommitID()
	require.Equal(t, expectedHeight, lastHeight)
	require.Equal(t, expectedID, lastID)
}

func TestOptionFunction(t *testing.T) {
	logger := defaultLogger()
	db := dbm.NewMemDB()
	bap := NewBaseApp("starting name", logger, db, nil, testChangeNameHelper("new name"))
	require.Equal(t, bap.name, "new name", "BaseApp should have had name changed via option function")
}

func testChangeNameHelper(name string) func(*BaseApp) {
	return func(bap *BaseApp) {
		bap.name = name
	}
}

// Test that Info returns the latest committed state.
func TestInfo(t *testing.T) {
	app := newBaseApp(t.Name())

	// ----- test an empty response -------
	reqInfo := abci.RequestInfo{}
	res := app.Info(reqInfo)

	// should be empty
	assert.Equal(t, "", res.AppVersion)
	assert.Equal(t, t.Name(), res.Data)
	assert.Equal(t, int64(0), res.LastBlockHeight)
	require.Equal(t, []uint8(nil), res.LastBlockAppHash)

	// ----- test a proper response -------
	// TODO
}

func TestBaseAppOptionSeal(t *testing.T) {
	app := setupBaseApp(t)

	require.Panics(t, func() {
		app.SetName("")
	})
	require.Panics(t, func() {
		app.SetAppVersion("")
	})
	require.Panics(t, func() {
		app.SetDB(nil)
	})
	require.Panics(t, func() {
		app.SetCMS(nil)
	})
	require.Panics(t, func() {
		app.SetInitChainer(nil)
	})
	require.Panics(t, func() {
		app.SetBeginBlocker(nil)
	})
	require.Panics(t, func() {
		app.SetEndBlocker(nil)
	})
	require.Panics(t, func() {
		app.SetAnteHandler(nil)
	})
}

func TestSetMinGasPrices(t *testing.T) {
	minGasPrices, err := ParseGasPrices("5000stake/10gas")
	require.Nil(t, err)
	app := newBaseApp(t.Name(), SetMinGasPrices("5000stake/10gas"))
	require.Equal(t, minGasPrices, app.minGasPrices)
}

func TestInitChainer(t *testing.T) {
	name := t.Name()
	// keep the db and logger ourselves so
	// we can reload the same  app later
	db := dbm.NewMemDB()
	logger := defaultLogger()
	app := NewBaseApp(name, logger, db, nil)
	capKey := store.NewStoreKey(MainStoreKey)
	capKey2 := store.NewStoreKey("key2")
	app.MountStoreWithDB(capKey1, iavl.StoreConstructor, nil)
	app.MountStoreWithDB(capKey2, iavl.StoreConstructor, nil)

	// set a value in the store on init chain
	key, value := []byte("hello"), []byte("goodbye")
	var initChainer InitChainer = func(ctx Context, req abci.RequestInitChain) abci.ResponseInitChain {
		store := ctx.Store(capKey)
		store.Set(key, value)
		return abci.ResponseInitChain{}
	}

	query := abci.RequestQuery{
		Path: "/store/main/key",
		Data: key,
	}

	// initChainer is nil - nothing happens
	app.InitChain(abci.RequestInitChain{})
	res := app.Query(query)
	require.Equal(t, 0, len(res.Value))

	// set initChainer and try again - should see the value
	app.SetInitChainer(initChainer)

	// stores are mounted and private members are set - sealing baseapp
	err := app.LoadLatestVersion(capKey) // needed to make stores non-nil
	require.Nil(t, err)
	require.Equal(t, int64(0), app.LastBlockHeight())

	app.InitChain(abci.RequestInitChain{AppStateBytes: []byte("{}"), ChainID: "test-chain-id"}) // must have valid JSON genesis file, even if empty

	// assert that chainID is set correctly in InitChain
	chainID := app.deliverState.ctx.ChainID()
	require.Equal(t, "test-chain-id", chainID, "ChainID in deliverState not set correctly in InitChain")

	chainID = app.checkState.ctx.ChainID()
	require.Equal(t, "test-chain-id", chainID, "ChainID in checkState not set correctly in InitChain")

	app.Commit()
	res = app.Query(query)
	require.Equal(t, int64(1), app.LastBlockHeight())
	require.Equal(t, value, res.Value)

	// reload app
	app = NewBaseApp(name, logger, db, nil)
	app.SetInitChainer(initChainer)
	app.MountStoreWithDB(capKey, iavl.StoreConstructor, nil)
	app.MountStoreWithDB(capKey2, iavl.StoreConstructor, nil)
	err = app.LoadLatestVersion(capKey) // needed to make stores non-nil
	require.Nil(t, err)
	require.Equal(t, int64(1), app.LastBlockHeight())

	// ensure we can still query after reloading
	res = app.Query(query)
	require.Equal(t, value, res.Value)

	// commit and ensure we can still query
	header := &bft.Header{Height: app.LastBlockHeight() + 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})
	app.Commit()

	res = app.Query(query)
	require.Equal(t, value, res.Value)
}

type testTxData struct {
	FailOnAnte bool
	Counter    int64
}

func getFailOnAnte(tx Tx) bool {
	var testdata testTxData
	amino.MustUnmarshalJSON([]byte(tx.Memo), &testdata)
	return testdata.FailOnAnte
}

func setFailOnAnte(tx *Tx, fail bool) {
	var testdata testTxData
	amino.MustUnmarshalJSON([]byte(tx.Memo), &testdata)
	testdata.FailOnAnte = fail
	tx.Memo = string(amino.MustMarshalJSON(testdata))
}

func getCounter(tx Tx) int64 {
	var testdata testTxData
	amino.MustUnmarshalJSON([]byte(tx.Memo), &testdata)
	return testdata.Counter
}

func setCounter(tx *Tx, counter int64) {
	var testdata testTxData
	amino.MustUnmarshalJSON([]byte(tx.Memo), &testdata)
	testdata.Counter = counter
	tx.Memo = string(amino.MustMarshalJSON(testdata))
}

func setFailOnHandler(tx *Tx, fail bool) {
	for i, msg := range tx.Msgs {
		tx.Msgs[i] = msgCounter{msg.(msgCounter).Counter, fail}
	}
}

const (
	routeMsgCounter  = "msgCounter"
	routeMsgCounter2 = "msgCounter2"
)

// ValidateBasic() fails on negative counters.
// Otherwise it's up to the handlers
type msgCounter struct {
	Counter       int64
	FailOnHandler bool
}

// Implements Msg
func (msg msgCounter) Route() string                { return routeMsgCounter }
func (msg msgCounter) Type() string                 { return "counter1" }
func (msg msgCounter) GetSignBytes() []byte         { return nil }
func (msg msgCounter) GetSigners() []crypto.Address { return nil }
func (msg msgCounter) ValidateBasic() error {
	if msg.Counter >= 0 {
		return nil
	}
	return std.ErrInvalidSequence("counter should be a non-negative integer.")
}

func newTxCounter(txInt int64, msgInts ...int64) std.Tx {
	var msgs []Msg
	for _, msgInt := range msgInts {
		msgs = append(msgs, msgCounter{msgInt, false})
	}
	tx := std.Tx{Msgs: msgs}
	setCounter(&tx, txInt)
	setFailOnHandler(&tx, false)
	return tx
}

// a msg we dont know how to route
type msgNoRoute struct {
	msgCounter
}

func (tx msgNoRoute) Route() string { return "noroute" }

// a msg we dont know how to decode
type msgNoDecode struct {
	msgCounter
}

func (tx msgNoDecode) Route() string { return routeMsgCounter }

// Another counter msg. Duplicate of msgCounter
type msgCounter2 struct {
	Counter int64
}

// Implements Msg
func (msg msgCounter2) Route() string                { return routeMsgCounter2 }
func (msg msgCounter2) Type() string                 { return "counter2" }
func (msg msgCounter2) GetSignBytes() []byte         { return nil }
func (msg msgCounter2) GetSigners() []crypto.Address { return nil }
func (msg msgCounter2) ValidateBasic() error {
	if msg.Counter >= 0 {
		return nil
	}
	return std.ErrInvalidSequence("counter should be a non-negative integer.")
}

func anteHandlerTxTest(t *testing.T, capKey store.StoreKey, storeKey []byte) AnteHandler {
	return func(ctx Context, tx std.Tx, simulate bool) (newCtx Context, res Result, abort bool) {
		store := ctx.Store(capKey)
		if getFailOnAnte(tx) {
			res.Error = toABCIError(std.ErrInternal("ante handler failure"))
			return newCtx, res, true
		}

		res = incrementingCounter(t, store, storeKey, getCounter(tx))
		return
	}
}

type testHandler struct {
	process func(Context, Msg) Result
	query   func(Context, abci.RequestQuery) abci.ResponseQuery
}

func (th testHandler) Process(ctx Context, msg Msg) Result {
	return th.process(ctx, msg)
}

func (th testHandler) Query(ctx Context, req abci.RequestQuery) abci.ResponseQuery {
	return th.query(ctx, req)
}

func newTestHandler(proc func(Context, Msg) Result) Handler {
	return testHandler{
		process: proc,
	}
}

type msgCounterHandler struct {
	t          *testing.T
	capKey     store.StoreKey
	deliverKey []byte
}

func newMsgCounterHandler(t *testing.T, capKey store.StoreKey, deliverKey []byte) Handler {
	return msgCounterHandler{t, capKey, deliverKey}
}

func (mch msgCounterHandler) Process(ctx Context, msg Msg) (res Result) {
	store := ctx.Store(mch.capKey)
	var msgCount int64
	switch m := msg.(type) {
	case *msgCounter:
		if m.FailOnHandler {
			res.Error = toABCIError(std.ErrInternal("message handler failure"))
			return
		}

		msgCount = m.Counter
	case *msgCounter2:
		msgCount = m.Counter
	}

	return incrementingCounter(mch.t, store, mch.deliverKey, msgCount)
}

func (mch msgCounterHandler) Query(ctx Context, req abci.RequestQuery) abci.ResponseQuery {
	panic("should not happen")
}

func i2b(i int64) []byte {
	return []byte{byte(i)}
}

func getIntFromStore(store store.Store, key []byte) int64 {
	bz := store.Get(key)
	if len(bz) == 0 {
		return 0
	}
	i, err := binary.ReadVarint(bytes.NewBuffer(bz))
	if err != nil {
		panic(err)
	}
	return i
}

func setIntOnStore(store store.Store, key []byte, i int64) {
	bz := make([]byte, 8)
	n := binary.PutVarint(bz, i)
	store.Set(key, bz[:n])
}

// check counter matches what's in store.
// increment and store
func incrementingCounter(t *testing.T, store store.Store, counterKey []byte, counter int64) (res Result) {
	storedCounter := getIntFromStore(store, counterKey)
	require.Equal(t, storedCounter, counter)
	setIntOnStore(store, counterKey, counter+1)
	return
}

//---------------------------------------------------------------------
// Tx processing - CheckTx, DeliverTx, SimulateTx.
// These tests use the serialized tx as input, while most others will use the
// Check(), Deliver(), Simulate() methods directly.
// Ensure that Check/Deliver/Simulate work as expected with the store.

// Test that successive CheckTx can see each others' effects
// on the store within a block, and that the CheckTx state
// gets reset to the latest committed state during Commit
func TestCheckTx(t *testing.T) {
	// This ante handler reads the key and checks that the value matches the current counter.
	// This ensures changes to the kvstore persist across successive CheckTx.
	counterKey := []byte("counter-key")

	anteOpt := func(bapp *BaseApp) { bapp.SetAnteHandler(anteHandlerTxTest(t, capKey1, counterKey)) }
	routerOpt := func(bapp *BaseApp) {
		// TODO: can remove this once CheckTx doesnt process msgs.
		bapp.Router().AddRoute(routeMsgCounter, newTestHandler(func(ctx Context, msg Msg) Result { return Result{} }))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)

	nTxs := int64(5)
	app.InitChain(abci.RequestInitChain{})

	for i := int64(0); i < nTxs; i++ {
		tx := newTxCounter(i, 0)
		txBytes, err := amino.MarshalSized(tx)
		require.NoError(t, err)
		r := app.CheckTx(abci.RequestCheckTx{Tx: txBytes})
		assert.True(t, r.IsOK(), fmt.Sprintf("%v", r))
	}

	checkStateStore := app.checkState.ctx.Store(capKey1)
	storedCounter := getIntFromStore(checkStateStore, counterKey)

	// Ensure AnteHandler ran
	require.Equal(t, nTxs, storedCounter)

	// If a block is committed, CheckTx state should be reset.
	header := &bft.Header{Height: 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})
	app.EndBlock(abci.RequestEndBlock{})
	app.Commit()

	checkStateStore = app.checkState.ctx.Store(capKey1)
	storedBytes := checkStateStore.Get(counterKey)
	require.Nil(t, storedBytes)
}

// Test that successive DeliverTx can see each others' effects
// on the store, both within and across blocks.
func TestDeliverTx(t *testing.T) {
	// test increments in the ante
	anteKey := []byte("ante-key")
	anteOpt := func(bapp *BaseApp) { bapp.SetAnteHandler(anteHandlerTxTest(t, capKey1, anteKey)) }

	// test increments in the handler
	deliverKey := []byte("deliver-key")
	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newMsgCounterHandler(t, capKey1, deliverKey))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)
	app.InitChain(abci.RequestInitChain{})

	nBlocks := 3
	txPerHeight := 5

	for blockN := 0; blockN < nBlocks; blockN++ {
		header := &bft.Header{Height: int64(blockN) + 1}
		app.BeginBlock(abci.RequestBeginBlock{Header: header})

		for i := 0; i < txPerHeight; i++ {
			counter := int64(blockN*txPerHeight + i)
			tx := newTxCounter(counter, counter)

			txBytes, err := amino.MarshalSized(tx)
			require.NoError(t, err)

			res := app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
			require.True(t, res.IsOK(), fmt.Sprintf("%v", res))
		}

		app.EndBlock(abci.RequestEndBlock{})
		app.Commit()
	}
}

// Number of messages doesn't matter to CheckTx.
func TestMultiMsgCheckTx(t *testing.T) {
	// TODO: ensure we get the same results
	// with one message or many
}

// One call to DeliverTx should process all the messages, in order.
func TestMultiMsgDeliverTx(t *testing.T) {
	// increment the tx counter
	anteKey := []byte("ante-key")
	anteOpt := func(bapp *BaseApp) { bapp.SetAnteHandler(anteHandlerTxTest(t, capKey1, anteKey)) }

	// increment the msg counter
	deliverKey := []byte("deliver-key")
	deliverKey2 := []byte("deliver-key2")
	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newMsgCounterHandler(t, capKey1, deliverKey))
		bapp.Router().AddRoute(routeMsgCounter2, newMsgCounterHandler(t, capKey1, deliverKey2))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)

	// run a multi-msg tx
	// with all msgs the same route

	header := &bft.Header{Height: 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})
	tx := newTxCounter(0, 0, 1, 2)
	txBytes, err := amino.MarshalSized(tx)
	require.NoError(t, err)
	res := app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
	require.True(t, res.IsOK(), fmt.Sprintf("%v", res))

	store := app.deliverState.ctx.Store(capKey1)

	// tx counter only incremented once
	txCounter := getIntFromStore(store, anteKey)
	require.Equal(t, int64(1), txCounter)

	// msg counter incremented three times
	msgCounter := getIntFromStore(store, deliverKey)
	require.Equal(t, int64(3), msgCounter)

	// replace the second message with a msgCounter2

	tx = newTxCounter(1, 3)
	tx.Msgs = append(tx.Msgs, msgCounter2{0})
	tx.Msgs = append(tx.Msgs, msgCounter2{1})
	txBytes, err = amino.MarshalSized(tx)
	require.NoError(t, err)
	res = app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
	require.True(t, res.IsOK(), fmt.Sprintf("%v", res))

	store = app.deliverState.ctx.Store(capKey1)

	// tx counter only incremented once
	txCounter = getIntFromStore(store, anteKey)
	require.Equal(t, int64(2), txCounter)

	// original counter increments by one
	// new counter increments by two
	msgCounter = getIntFromStore(store, deliverKey)
	require.Equal(t, int64(4), msgCounter)
	msgCounter2 := getIntFromStore(store, deliverKey2)
	require.Equal(t, int64(2), msgCounter2)
}

// Interleave calls to Check and Deliver and ensure
// that there is no cross-talk. Check sees results of the previous Check calls
// and Deliver sees that of the previous Deliver calls, but they don't see eachother.
func TestConcurrentCheckDeliver(t *testing.T) {
	// TODO
}

// Simulate a transaction that uses gas to compute the gas.
// Simulate() and Query("/app/simulate", txBytes) should give
// the same results.
func TestSimulateTx(t *testing.T) {
	gasConsumed := int64(5)

	anteOpt := func(bapp *BaseApp) {
		bapp.SetAnteHandler(func(ctx Context, tx Tx, simulate bool) (newCtx Context, res Result, abort bool) {
			newCtx = ctx.WithGasMeter(store.NewGasMeter(gasConsumed))
			return
		})
	}

	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newTestHandler(func(ctx Context, msg Msg) Result {
			ctx.GasMeter().ConsumeGas(gasConsumed, "test")
			return Result{GasUsed: ctx.GasMeter().GasConsumed()}
		}))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)

	app.InitChain(abci.RequestInitChain{})

	nBlocks := 3
	for blockN := 0; blockN < nBlocks; blockN++ {
		count := int64(blockN + 1)
		header := &bft.Header{Height: count}
		app.BeginBlock(abci.RequestBeginBlock{Header: header})

		tx := newTxCounter(count, count)
		txBytes, err := amino.MarshalSized(tx)
		require.Nil(t, err)

		// simulate a message, check gas reported
		result := app.Simulate(txBytes, tx)
		require.True(t, result.IsOK(), result.Log)
		require.Equal(t, gasConsumed, result.GasUsed)

		// simulate again, same result
		result = app.Simulate(txBytes, tx)
		require.True(t, result.IsOK(), result.Log)
		require.Equal(t, gasConsumed, result.GasUsed)

		// simulate by calling Query with encoded tx
		query := abci.RequestQuery{
			Path: "/app/simulate",
			Data: txBytes,
		}
		queryResult := app.Query(query)
		require.True(t, queryResult.IsOK(), queryResult.Log)

		var res Result
		amino.MustUnmarshalSized(queryResult.Value, &res)
		require.Nil(t, err, "Result unmarshalling failed")
		require.True(t, res.IsOK(), res.Log)
		require.Equal(t, gasConsumed, res.GasUsed, res.Log)
		app.EndBlock(abci.RequestEndBlock{})
		app.Commit()
	}
}

func TestRunInvalidTransaction(t *testing.T) {
	anteOpt := func(bapp *BaseApp) {
		bapp.SetAnteHandler(func(ctx Context, tx Tx, simulate bool) (newCtx Context, res Result, abort bool) {
			return
		})
	}
	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newTestHandler(func(ctx Context, msg Msg) (res Result) { return }))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)

	header := &bft.Header{Height: 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})

	// Transaction with no messages
	{
		emptyTx := std.Tx{}
		err := app.Deliver(emptyTx)
		_, ok := err.Error.(std.UnknownRequestError)
		require.True(t, ok)
	}

	// Transaction where ValidateBasic fails
	{
		testCases := []struct {
			tx   std.Tx
			fail bool
		}{
			{newTxCounter(0, 0), false},
			{newTxCounter(-1, 0), false},
			{newTxCounter(100, 100), false},
			{newTxCounter(100, 5, 4, 3, 2, 1), false},

			{newTxCounter(0, -1), true},
			{newTxCounter(0, 1, -2), true},
			{newTxCounter(0, 1, 2, -10, 5), true},
		}

		for _, testCase := range testCases {
			tx := testCase.tx
			res := app.Deliver(tx)
			if testCase.fail {
				_, ok := res.Error.(std.InvalidSequenceError)
				require.True(t, ok)
			} else {
				require.True(t, res.IsOK(), fmt.Sprintf("%v", res))
			}
		}
	}

	// Transaction with no known route
	{
		unknownRouteTx := std.Tx{Msgs: []Msg{msgNoRoute{}}}
		err := app.Deliver(unknownRouteTx)
		_, ok := err.Error.(std.UnknownRequestError)
		require.True(t, ok)

		unknownRouteTx = std.Tx{Msgs: []Msg{msgCounter{}, msgNoRoute{}}}
		err = app.Deliver(unknownRouteTx)
		_, ok = err.Error.(std.UnknownRequestError)
		require.True(t, ok)
	}

	// Transaction with an unregistered message
	{
		tx := newTxCounter(0, 0)
		tx.Msgs = append(tx.Msgs, msgNoDecode{})

		txBytes, err := amino.MarshalSized(tx)
		require.NoError(t, err)
		res := app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
		_, ok := res.Error.(std.TxDecodeError)
		require.True(t, ok)
	}
}

// Test that transactions exceeding gas limits fail
func TestTxGasLimits(t *testing.T) {
	gasGranted := int64(10)
	anteOpt := func(bapp *BaseApp) {
		bapp.SetAnteHandler(func(ctx Context, tx Tx, simulate bool) (newCtx Context, res Result, abort bool) {
			newCtx = ctx.WithGasMeter(store.NewGasMeter(gasGranted))

			defer func() {
				if r := recover(); r != nil {
					var err error
					var ok bool
					if err, ok = r.(error); !ok {
						err = errors.New("XXX %v", r)
					}
					switch cerr := toABCIError(err).(type) {
					case std.OutOfGasError:
						log := fmt.Sprintf("out of gas in location: %v", "unknown") // cerr.Descriptor)
						res.Error = cerr
						res.Log = log
						res.GasWanted = gasGranted
						res.GasUsed = newCtx.GasMeter().GasConsumed()
					default:
						panic(r)
					}
				}
			}()

			count := getCounter(tx)
			newCtx.GasMeter().ConsumeGas(int64(count), "counter-ante")
			res = Result{
				GasWanted: gasGranted,
			}
			return
		})

	}

	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newTestHandler(func(ctx Context, msg Msg) Result {
			count := msg.(msgCounter).Counter
			ctx.GasMeter().ConsumeGas(int64(count), "counter-handler")
			return Result{}
		}))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)

	header := &bft.Header{Height: 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})

	testCases := []struct {
		tx      std.Tx
		gasUsed int64
		fail    bool
	}{
		{newTxCounter(0, 0), 0, false},
		{newTxCounter(1, 1), 2, false},
		{newTxCounter(9, 1), 10, false},
		{newTxCounter(1, 9), 10, false},
		{newTxCounter(10, 0), 10, false},
		{newTxCounter(0, 10), 10, false},
		{newTxCounter(0, 8, 2), 10, false},
		{newTxCounter(0, 5, 1, 1, 1, 1, 1), 10, false},
		{newTxCounter(0, 5, 1, 1, 1, 1), 9, false},

		{newTxCounter(9, 2), 11, true},
		{newTxCounter(2, 9), 11, true},
		{newTxCounter(9, 1, 1), 11, true},
		{newTxCounter(1, 8, 1, 1), 11, true},
		{newTxCounter(11, 0), 11, true},
		{newTxCounter(0, 11), 11, true},
		{newTxCounter(0, 5, 11), 16, true},
	}

	for i, tc := range testCases {
		tx := tc.tx
		res := app.Deliver(tx)

		// check gas used and wanted
		require.Equal(t, tc.gasUsed, res.GasUsed, fmt.Sprintf("%d: %v, %v", i, tc, res))

		// check for out of gas
		if !tc.fail {
			require.True(t, res.IsOK(), fmt.Sprintf("%d: %v, %v", i, tc, res))
		} else {
			_, ok := res.Error.(std.OutOfGasError)
			require.True(t, ok, fmt.Sprintf("%d: %v, %v", i, tc, res))
		}
	}
}

// Test that transactions exceeding gas limits fail
func TestMaxBlockGasLimits(t *testing.T) {
	gasGranted := int64(10)
	anteOpt := func(bapp *BaseApp) {
		bapp.SetAnteHandler(func(ctx Context, tx Tx, simulate bool) (newCtx Context, res Result, abort bool) {
			newCtx = ctx.WithGasMeter(store.NewGasMeter(gasGranted))

			defer func() {
				if r := recover(); r != nil {
					var err error
					var ok bool
					if err, ok = r.(error); !ok {
						err = errors.New("XXX %v", r)
					}
					switch cerr := toABCIError(err).(type) {
					case std.OutOfGasError:
						log := fmt.Sprintf("out of gas in location: %v", "unknown") // rType.Descriptor)
						res.Error = cerr
						res.Log = log
						res.GasWanted = gasGranted
						res.GasUsed = newCtx.GasMeter().GasConsumed()
					default:
						panic(r)
					}
				}
			}()

			count := getCounter(tx)
			newCtx.GasMeter().ConsumeGas(int64(count), "counter-ante")
			res = Result{
				GasWanted: gasGranted,
			}
			return
		})

	}

	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newTestHandler(func(ctx Context, msg Msg) Result {
			count := msg.(msgCounter).Counter
			ctx.GasMeter().ConsumeGas(int64(count), "counter-handler")
			return Result{}
		}))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)
	app.InitChain(abci.RequestInitChain{
		ConsensusParams: &abci.ConsensusParams{
			Block: &abci.BlockParams{
				MaxGas: 100,
			},
		},
	})

	testCases := []struct {
		tx                std.Tx
		numDelivers       int
		gasUsedPerDeliver int64
		fail              bool
		failAfterDeliver  int
	}{
		{newTxCounter(0, 0), 0, 0, false, 0},
		{newTxCounter(9, 1), 2, 10, false, 0},
		{newTxCounter(10, 0), 3, 10, false, 0},
		{newTxCounter(10, 0), 10, 10, false, 0},
		{newTxCounter(2, 7), 11, 9, false, 0},
		{newTxCounter(10, 0), 10, 10, false, 0}, // hit the limit but pass

		{newTxCounter(10, 0), 11, 10, true, 10},
		{newTxCounter(10, 0), 15, 10, true, 10},
		{newTxCounter(9, 0), 12, 9, true, 11}, // fly past the limit
	}

	for i, tc := range testCases {
		fmt.Printf("debug i: %v\n", i)
		tx := tc.tx

		// reset the block gas
		header := &bft.Header{Height: app.LastBlockHeight() + 1}
		app.BeginBlock(abci.RequestBeginBlock{Header: header})

		// execute the transaction multiple times
		for j := 0; j < tc.numDelivers; j++ {
			res := app.Deliver(tx)

			ctx := app.getState(runTxModeDeliver).ctx
			blockGasUsed := ctx.BlockGasMeter().GasConsumed()

			// check for failed transactions
			if tc.fail && (j+1) > tc.failAfterDeliver {
				_, ok := res.Error.(std.OutOfGasError)
				require.True(t, ok, fmt.Sprintf("%d: %v, %v", i, tc, res))
				require.True(t, ctx.BlockGasMeter().IsOutOfGas())
			} else {
				// check gas used and wanted
				expBlockGasUsed := tc.gasUsedPerDeliver * int64(j+1)
				require.Equal(t, expBlockGasUsed, blockGasUsed,
					fmt.Sprintf("%d,%d: %v, %v, %v, %v", i, j, tc, expBlockGasUsed, blockGasUsed, res))

				require.True(t, res.IsOK(), fmt.Sprintf("%d,%d: %v, %v", i, j, tc, res))
				require.False(t, ctx.BlockGasMeter().IsPastLimit())
			}
		}
	}
}

func TestBaseAppAnteHandler(t *testing.T) {
	anteKey := []byte("ante-key")
	anteOpt := func(bapp *BaseApp) {
		bapp.SetAnteHandler(anteHandlerTxTest(t, capKey1, anteKey))
	}

	deliverKey := []byte("deliver-key")
	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newMsgCounterHandler(t, capKey1, deliverKey))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)

	app.InitChain(abci.RequestInitChain{})

	header := &bft.Header{Height: app.LastBlockHeight() + 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})

	// execute a tx that will fail ante handler execution
	//
	// NOTE: State should not be mutated here. This will be implicitly checked by
	// the next txs ante handler execution (anteHandlerTxTest).
	tx := newTxCounter(0, 0)
	setFailOnAnte(&tx, true)
	txBytes, err := amino.MarshalSized(tx)
	require.NoError(t, err)
	res := app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
	require.False(t, res.IsOK(), fmt.Sprintf("%v", res))

	ctx := app.getState(runTxModeDeliver).ctx
	store := ctx.Store(capKey1)
	require.Equal(t, int64(0), getIntFromStore(store, anteKey))

	// execute at tx that will pass the ante handler (the checkTx state should
	// mutate) but will fail the message handler
	tx = newTxCounter(0, 0)
	setFailOnHandler(&tx, true)

	txBytes, err = amino.MarshalSized(tx)
	require.NoError(t, err)

	res = app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
	require.False(t, res.IsOK(), fmt.Sprintf("%v", res))

	ctx = app.getState(runTxModeDeliver).ctx
	store = ctx.Store(capKey1)
	require.Equal(t, int64(1), getIntFromStore(store, anteKey))
	require.Equal(t, int64(0), getIntFromStore(store, deliverKey))

	// execute a successful ante handler and message execution where state is
	// implicitly checked by previous tx executions
	tx = newTxCounter(1, 0)

	txBytes, err = amino.MarshalSized(tx)
	require.NoError(t, err)

	res = app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
	require.True(t, res.IsOK(), fmt.Sprintf("%v", res))

	ctx = app.getState(runTxModeDeliver).ctx
	store = ctx.Store(capKey1)
	require.Equal(t, int64(2), getIntFromStore(store, anteKey))
	require.Equal(t, int64(1), getIntFromStore(store, deliverKey))

	// commit
	app.EndBlock(abci.RequestEndBlock{})
	app.Commit()
}

func TestGasConsumptionBadTx(t *testing.T) {
	gasWanted := int64(5)
	anteOpt := func(bapp *BaseApp) {
		bapp.SetAnteHandler(func(ctx Context, tx Tx, simulate bool) (newCtx Context, res Result, abort bool) {
			newCtx = ctx.WithGasMeter(store.NewGasMeter(gasWanted))

			defer func() {
				if r := recover(); r != nil {
					var err error
					var ok bool
					if err, ok = r.(error); !ok {
						err = errors.New("XXX %v", r)
					}
					switch cerr := toABCIError(err).(type) {
					case std.OutOfGasError:
						log := fmt.Sprintf("out of gas in location: %v", "unknown") // rType.Descriptor)
						res.Error = cerr
						res.Log = log
						res.GasWanted = gasWanted
						res.GasUsed = newCtx.GasMeter().GasConsumed()
					default:
						panic(r)
					}
				}
			}()

			newCtx.GasMeter().ConsumeGas(int64(getCounter(tx)), "counter-ante")
			if getFailOnAnte(tx) {
				res.Error = toABCIError(std.ErrInternal("ante handler failure"))
				return newCtx, res, true
			}

			res = Result{
				GasWanted: gasWanted,
			}
			return
		})
	}

	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newTestHandler(func(ctx Context, msg Msg) Result {
			count := msg.(msgCounter).Counter
			ctx.GasMeter().ConsumeGas(int64(count), "counter-handler")
			return Result{}
		}))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)
	app.InitChain(abci.RequestInitChain{
		ConsensusParams: &abci.ConsensusParams{
			Block: &abci.BlockParams{
				MaxGas: 9,
			},
		},
	})

	app.InitChain(abci.RequestInitChain{})

	header := &bft.Header{Height: app.LastBlockHeight() + 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})

	tx := newTxCounter(5, 0)
	setFailOnAnte(&tx, true)
	txBytes, err := amino.MarshalSized(tx)
	require.NoError(t, err)

	res := app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
	require.False(t, res.IsOK(), fmt.Sprintf("%v", res))

	// require next tx to fail due to black gas limit
	tx = newTxCounter(5, 0)
	txBytes, err = amino.MarshalSized(tx)
	require.NoError(t, err)

	res = app.DeliverTx(abci.RequestDeliverTx{Tx: txBytes})
	require.False(t, res.IsOK(), fmt.Sprintf("%v", res))
}

// Test that we can only query from the latest committed state.
func TestQuery(t *testing.T) {
	key, value := []byte("hello"), []byte("goodbye")
	anteOpt := func(bapp *BaseApp) {
		bapp.SetAnteHandler(func(ctx Context, tx Tx, simulate bool) (newCtx Context, res Result, abort bool) {
			store := ctx.Store(capKey1)
			store.Set(key, value)
			return
		})
	}

	routerOpt := func(bapp *BaseApp) {
		bapp.Router().AddRoute(routeMsgCounter, newTestHandler(func(ctx Context, msg Msg) Result {
			store := ctx.Store(capKey1)
			store.Set(key, value)
			return Result{}
		}))
	}

	app := setupBaseApp(t, anteOpt, routerOpt)

	app.InitChain(abci.RequestInitChain{})

	// NOTE: "/store/key1" tells us Store
	// and the final "/key" says to use the data as the
	// key in the given Store ...
	query := abci.RequestQuery{
		Path: "/store/key1/key",
		Data: key,
	}
	tx := newTxCounter(0, 0)

	// query is empty before we do anything
	res := app.Query(query)
	require.Equal(t, 0, len(res.Value))

	// query is still empty after a CheckTx
	resTx := app.Check(tx)
	require.True(t, resTx.IsOK(), fmt.Sprintf("%v", resTx))
	res = app.Query(query)
	require.Equal(t, 0, len(res.Value))

	// query is still empty after a DeliverTx before we commit
	header := &bft.Header{Height: app.LastBlockHeight() + 1}
	app.BeginBlock(abci.RequestBeginBlock{Header: header})

	resTx = app.Deliver(tx)
	require.True(t, resTx.IsOK(), fmt.Sprintf("%v", resTx))
	res = app.Query(query)
	require.Equal(t, 0, len(res.Value))

	// query returns correct value after Commit
	app.Commit()
	res = app.Query(query)
	require.Equal(t, value, res.Value)
}

func TestGetMaximumBlockGas(t *testing.T) {
	app := setupBaseApp(t)

	app.setConsensusParams(&abci.ConsensusParams{Block: &abci.BlockParams{MaxGas: 0}})
	require.Equal(t, int64(0), app.getMaximumBlockGas())

	app.setConsensusParams(&abci.ConsensusParams{Block: &abci.BlockParams{MaxGas: -1}})
	require.Equal(t, int64(0), app.getMaximumBlockGas())

	app.setConsensusParams(&abci.ConsensusParams{Block: &abci.BlockParams{MaxGas: 5000000}})
	require.Equal(t, int64(5000000), app.getMaximumBlockGas())

	app.setConsensusParams(&abci.ConsensusParams{Block: &abci.BlockParams{MaxGas: -5000000}})
	require.Panics(t, func() { app.getMaximumBlockGas() })
}