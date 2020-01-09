package history

import (
	"fmt"
	"math"
	"reflect"

	sq "github.com/Masterminds/squirrel"
	"github.com/stellar/go/services/horizon/internal/db2"
	"github.com/stellar/go/services/horizon/internal/toid"
	"github.com/stellar/go/support/errors"
	supportTime "github.com/stellar/go/support/time"
	"github.com/stellar/go/xdr"
)

// PagingToken returns a cursor for this trade
func (r *Trade) PagingToken() string {
	return fmt.Sprintf("%d-%d", r.HistoryOperationID, r.Order)
}

// HasPrice returns true if the trade has non-null price data
func (r *Trade) HasPrice() bool {
	return r.PriceN.Valid && r.PriceD.Valid
}

// Trades provides a helper to filter rows from the `history_trades` table
// with pre-defined filters.  See `TradesQ` methods for the available filters.
func (q *Q) Trades() *TradesQ {
	return &TradesQ{
		parent: q,
		sql: joinTradeAssets(
			joinTradeAccounts(
				selectTradeFields.From("history_trades htrd"),
				"history_accounts",
			),
			"history_assets",
		),
	}
}

func (q *Q) expTrades() *TradesQ {
	return &TradesQ{
		parent: q,
		sql: joinTradeAssets(
			joinTradeAccounts(
				selectTradeFields.From("exp_history_trades htrd"),
				"exp_history_accounts",
			),
			"exp_history_assets",
		),
	}
}

// ReverseTrades provides a helper to filter rows from the `history_trades` table
// with pre-defined filters and reversed base/counter.  See `TradesQ` methods for the available filters.
func (q *Q) ReverseTrades() *TradesQ {
	return &TradesQ{
		parent: q,
		sql: joinTradeAssets(
			joinTradeAccounts(
				selectReverseTradeFields.From("history_trades htrd"),
				"history_accounts",
			),
			"history_assets",
		),
	}
}

// TradesForAssetPair provides a helper to filter rows from the `history_trades` table
// with the base filter of a specific asset pair.  See `TradesQ` methods for further available filters.
func (q *Q) TradesForAssetPair(baseAssetId int64, counterAssetId int64) *TradesQ {
	orderPreserved, baseAssetId, counterAssetId := getCanonicalAssetOrder(baseAssetId, counterAssetId)
	var trades *TradesQ
	if orderPreserved {
		trades = q.Trades()
	} else {
		trades = q.ReverseTrades()
	}
	return trades.forAssetPair(baseAssetId, counterAssetId)
}

// ForOffer filters the query results by the offer id.
func (q *TradesQ) ForOffer(id int64) *TradesQ {
	q.sql = q.sql.Where("(htrd.base_offer_id = ? OR htrd.counter_offer_id = ?)", id, id)
	return q
}

//Filter by asset pair. This function is private to ensure that correct order and proper select statement are coupled
func (q *TradesQ) forAssetPair(baseAssetId int64, counterAssetId int64) *TradesQ {
	q.sql = q.sql.Where(sq.Eq{"base_asset_id": baseAssetId, "counter_asset_id": counterAssetId})
	return q
}

// ForLedger adds a filter which only includes trades within the given ledger sequence
func (q *TradesQ) ForLedger(sequence int32, order string) *TradesQ {
	from := toid.ID{LedgerSequence: sequence}.ToInt64()
	to := toid.ID{LedgerSequence: sequence + 1}.ToInt64()

	q.sql = q.sql.Where(
		"htrd.history_operation_id >= ? AND htrd.history_operation_id <= ? ",
		from,
		to,
	).OrderBy(
		"htrd.history_operation_id " + order + ", htrd.order " + order,
	)

	return q
}

// ForAccount filter Trades by account id
func (q *TradesQ) ForAccount(aid string) *TradesQ {
	var account Account
	q.Err = q.parent.AccountByAddress(&account, aid)
	if q.Err != nil {
		return q
	}

	q.sql = q.sql.Where("(htrd.base_account_id = ? OR htrd.counter_account_id = ?)", account.ID, account.ID)
	return q
}

// Page specifies the paging constraints for the query being built by `q`.
func (q *TradesQ) Page(page db2.PageQuery) *TradesQ {
	if q.Err != nil {
		return q
	}

	op, idx, err := page.CursorInt64Pair(db2.DefaultPairSep)
	if err != nil {
		q.Err = err
		return q
	}

	// constrain the second portion of the cursor pair to 32-bits
	if idx > math.MaxInt32 {
		idx = math.MaxInt32
	}

	// NOTE: Remember to test the queries below with EXPLAIN / EXPLAIN ANALYZE
	// before changing them.
	// This condition is using multicolumn index and it's easy to write it in a way that
	// DB will perform a full table scan.
	switch page.Order {
	case "asc":
		q.sql = q.sql.
			Where(`(
					 htrd.history_operation_id >= ?
				AND (
					 htrd.history_operation_id > ? OR
					(htrd.history_operation_id = ? AND htrd.order > ?)
				))`, op, op, op, idx).
			OrderBy("htrd.history_operation_id asc, htrd.order asc")
	case "desc":
		q.sql = q.sql.
			Where(`(
					 htrd.history_operation_id <= ?
				AND (
					 htrd.history_operation_id < ? OR
					(htrd.history_operation_id = ? AND htrd.order < ?)
				))`, op, op, op, idx).
			OrderBy("htrd.history_operation_id desc, htrd.order desc")
	}

	q.sql = q.sql.Limit(page.Limit)
	return q
}

// Select loads the results of the query specified by `q` into `dest`.
func (q *TradesQ) Select(dest interface{}) error {
	if q.Err != nil {
		return q.Err
	}

	q.Err = q.parent.Select(dest, q.sql)
	return q.Err
}

func joinTradeAccounts(selectBuilder sq.SelectBuilder, historyAccountsTable string) sq.SelectBuilder {
	return selectBuilder.
		Join(historyAccountsTable + " base_accounts ON base_account_id = base_accounts.id").
		Join(historyAccountsTable + " counter_accounts ON counter_account_id = counter_accounts.id")
}

func joinTradeAssets(selectBuilder sq.SelectBuilder, historyAssetsTable string) sq.SelectBuilder {
	return selectBuilder.
		Join(historyAssetsTable + " base_assets ON base_asset_id = base_assets.id").
		Join(historyAssetsTable + " counter_assets ON counter_asset_id = counter_assets.id")
}

var selectTradeFields = sq.Select(
	"history_operation_id",
	"htrd.\"order\"",
	"htrd.ledger_closed_at",
	"htrd.offer_id",
	"htrd.base_offer_id",
	"base_accounts.address as base_account",
	"base_assets.asset_type as base_asset_type",
	"base_assets.asset_code as base_asset_code",
	"base_assets.asset_issuer as base_asset_issuer",
	"htrd.base_amount",
	"htrd.counter_offer_id",
	"counter_accounts.address as counter_account",
	"counter_assets.asset_type as counter_asset_type",
	"counter_assets.asset_code as counter_asset_code",
	"counter_assets.asset_issuer as counter_asset_issuer",
	"htrd.counter_amount",
	"htrd.base_is_seller",
	"htrd.price_n",
	"htrd.price_d",
)

var selectReverseTradeFields = sq.Select(
	"history_operation_id",
	"htrd.\"order\"",
	"htrd.ledger_closed_at",
	"htrd.offer_id",
	"htrd.counter_offer_id as base_offer_id",
	"counter_accounts.address as base_account",
	"counter_assets.asset_type as base_asset_type",
	"counter_assets.asset_code as base_asset_code",
	"counter_assets.asset_issuer as base_asset_issuer",
	"htrd.counter_amount as base_amount",
	"htrd.base_offer_id as counter_offer_id",
	"base_accounts.address as counter_account",
	"base_assets.asset_type as counter_asset_type",
	"base_assets.asset_code as counter_asset_code",
	"base_assets.asset_issuer as counter_asset_issuer",
	"htrd.base_amount as counter_amount",
	"NOT(htrd.base_is_seller) as base_is_seller",
	"htrd.price_d as price_n",
	"htrd.price_n as price_d",
)

var tradesInsert = sq.Insert("history_trades").Columns(
	"history_operation_id",
	"\"order\"",
	"ledger_closed_at",
	"offer_id",
	"base_offer_id",
	"base_account_id",
	"base_asset_id",
	"base_amount",
	"counter_offer_id",
	"counter_account_id",
	"counter_asset_id",
	"counter_amount",
	"base_is_seller",
	"price_n",
	"price_d",
)

// Trade records a trade into the history_trades table
func (q *Q) InsertTrade(
	opid int64,
	order int32,
	buyer xdr.AccountId,
	buyOfferExists bool,
	buyOffer xdr.OfferEntry,
	trade xdr.ClaimOfferAtom,
	sellPrice xdr.Price,
	ledgerClosedAt supportTime.Millis,
) error {
	sellerAccountId, err := q.GetCreateAccountID(trade.SellerId)
	if err != nil {
		return errors.Wrap(err, "failed to load seller account id")
	}

	buyerAccountId, err := q.GetCreateAccountID(buyer)
	if err != nil {
		return errors.Wrap(err, "failed to load buyer account id")
	}

	soldAssetId, err := q.GetCreateAssetID(trade.AssetSold)
	if err != nil {
		return errors.Wrap(err, "failed to get sold asset id")
	}

	boughtAssetId, err := q.GetCreateAssetID(trade.AssetBought)
	if err != nil {
		return errors.Wrap(err, "failed to get bought asset id")
	}

	sellOfferId := EncodeOfferId(uint64(trade.OfferId), CoreOfferIDType)

	// if the buy offer exists, encode the stellar core generated id as the offer id
	// if not, encode the toid as the offer id
	var buyOfferId int64
	if buyOfferExists {
		buyOfferId = EncodeOfferId(uint64(buyOffer.OfferId), CoreOfferIDType)
	} else {
		buyOfferId = EncodeOfferId(uint64(opid), TOIDType)
	}

	orderPreserved, baseAssetId, counterAssetId := getCanonicalAssetOrder(soldAssetId, boughtAssetId)

	var baseAccountId, counterAccountId int64
	var baseAmount, counterAmount xdr.Int64
	var baseOfferId, counterOfferId int64

	if orderPreserved {
		baseAccountId = sellerAccountId
		baseAmount = trade.AmountSold
		counterAccountId = buyerAccountId
		counterAmount = trade.AmountBought
		baseOfferId = sellOfferId
		counterOfferId = buyOfferId
	} else {
		baseAccountId = buyerAccountId
		baseAmount = trade.AmountBought
		counterAccountId = sellerAccountId
		counterAmount = trade.AmountSold
		baseOfferId = buyOfferId
		counterOfferId = sellOfferId
		sellPrice.Invert()
	}

	sql := tradesInsert.Values(
		opid,
		order,
		ledgerClosedAt.ToTime(),
		trade.OfferId,
		baseOfferId,
		baseAccountId,
		baseAssetId,
		baseAmount,
		counterOfferId,
		counterAccountId,
		counterAssetId,
		counterAmount,
		orderPreserved,
		sellPrice.N,
		sellPrice.D,
	)

	_, err = q.Exec(sql)
	if err != nil {
		return errors.Wrap(err, "failed to exec sql")
	}
	return nil
}

func getCanonicalAssetOrder(assetId1 int64, assetId2 int64) (orderPreserved bool, baseAssetId int64, counterAssetId int64) {
	if assetId1 < assetId2 {
		return true, assetId1, assetId2
	} else {
		return false, assetId2, assetId1
	}
}

// CheckExpTrades checks that the trades in exp_history_trades
// for the given ledger matches the same transactions in history_trades
func (q *Q) CheckExpTrades(seq int32) (bool, error) {
	var trades, expTrades []Trade

	err := q.Trades().ForLedger(seq, "asc").Select(&trades)
	if err != nil {
		return false, err
	}

	err = q.expTrades().ForLedger(seq, "asc").Select(&expTrades)
	if err != nil {
		return false, err
	}

	// We only proceed with the comparison if we have trade data in both the
	// legacy ingestion system and the experimental ingestion system.
	// If there are no trades in either the legacy ingestion system or the
	// experimental ingestion system we skip the check.
	if len(trades) == 0 || len(expTrades) == 0 {
		return true, nil
	}

	if len(trades) != len(expTrades) {
		return false, nil
	}

	for i, trade := range trades {
		expTrade := expTrades[i]

		// compare LedgerCloseTime separately
		expClosedAt := expTrade.LedgerCloseTime
		expTrade.LedgerCloseTime = trade.LedgerCloseTime
		if expClosedAt.Unix() != trade.LedgerCloseTime.Unix() {
			return false, nil
		}

		// a given set of assets may not have the same ordering in history_assets and exp_history_assets
		// the ordering affects the value of BaseIsSeller (see how getCanonicalAssetOrder() is used above)
		if expTrade.BaseIsSeller != trade.BaseIsSeller {
			expTrade.BaseOfferID, expTrade.CounterOfferID = expTrade.CounterOfferID, expTrade.BaseOfferID
			expTrade.BaseAccount, expTrade.CounterAccount = expTrade.CounterAccount, expTrade.BaseAccount
			expTrade.BaseAssetType, expTrade.CounterAssetType = expTrade.CounterAssetType, expTrade.BaseAssetType
			expTrade.BaseAssetCode, expTrade.CounterAssetCode = expTrade.CounterAssetCode, expTrade.BaseAssetCode
			expTrade.BaseAssetIssuer, expTrade.CounterAssetIssuer = expTrade.CounterAssetIssuer, expTrade.BaseAssetIssuer
			expTrade.BaseAmount, expTrade.CounterAmount = expTrade.CounterAmount, expTrade.BaseAmount
			expTrade.PriceN, expTrade.PriceD = expTrade.PriceD, expTrade.PriceN

			expTrade.BaseIsSeller = trade.BaseIsSeller
		}

		if !reflect.DeepEqual(expTrade, trade) {
			return false, nil
		}
	}

	return true, nil
}
