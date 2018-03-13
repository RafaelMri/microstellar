package microstellar

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/url"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/stellar/go/build"
	"github.com/stellar/go/clients/horizon"
)

// OfferType tells ManagedOffer what operation to perform
type OfferType int

// The available offer types.
const (
	OfferCreate        = OfferType(0)
	OfferCreatePassive = OfferType(1)
	OfferUpdate        = OfferType(2)
	OfferDelete        = OfferType(3)
)

// OfferParams specify the parameters
type OfferParams struct {
	// Create, update, or delete.
	OfferType OfferType

	// The asset that's being sold on the DEX.
	SellAsset *Asset

	// The asset that you want to buy on the DEX.
	BuyAsset *Asset

	// How much you're willing to pay (in BuyAsset units) per unit of SellAsset.
	Price string

	// How many units of SellAsset are you selling?
	SellAmount string

	// Existing offer ID (for Update and Delete)
	OfferID string
}

// Offer is an offer on the DEX.
type Offer horizon.Offer

func newOfferFromHorizon(offer horizon.Offer) Offer {
	return Offer(offer)
}

// Link is typically embedded in a horizon response
type Link struct {
	Href      string `json:"href"`
	Templated bool   `json:"templated,omitempty"`
}

// PathResponse is what the horizon server returns
type PathResponse struct {
	Links struct {
		Self Link `json:"self"`
		Next Link `json:"next"`
		Prev Link `json:"prev"`
	} `json:"_links"`
	Embedded struct {
		Records []Path `json:"records"`
	} `json:"_embedded"`
}

// Path is a payment path
type Path struct {
	DestAmount        string `json:"destination_amount"`
	DestAssetCode     string `json:"destination_asset_code"`
	DestAssetIssuer   string `json:"destination_asset_issuer"`
	DestAssetType     string `json:"destination_asset_type"`
	SourceAmount      string `json:"source_amount"`
	SourceAssetCode   string `json:"source_asset_code"`
	SourceAssetIssuer string `json:"source_asset_issuer"`
	SourceAssetType   string `json:"source_asset_type"`
	Path              []struct {
		AssetCode   string `json:"asset_code"`
		AssetIssuer string `json:"asset_issuer"`
		AssetType   string `json:"asset_type"`
	} `json:"path"`
}

// LoadOffers returns all existing trade offers made by address.
func (ms *MicroStellar) LoadOffers(address string, options ...*Options) ([]Offer, error) {
	if err := ValidAddress(address); err != nil {
		return nil, errors.Errorf("invalid address: %s", address)
	}

	opt := mergeOptions(options)
	params := []interface{}{}

	if opt.hasLimit {
		params = append(params, horizon.Limit(opt.limit))
	}

	if opt.hasCursor {
		params = append(params, horizon.Cursor(opt.cursor))
	}

	if opt.sortDescending {
		params = append(params, horizon.Order("desc"))
	} else {
		params = append(params, horizon.Order("asc"))
	}

	debugf("LoadOffers", "loading offers for %s, with params +%v", address, params)
	if ms.fake {
		return []Offer{}, nil
	}

	tx := NewTx(ms.networkName, ms.params)
	horizonOffers, err := tx.GetClient().LoadAccountOffers(address, params...)

	if err != nil {
		return nil, errors.Wrap(err, "can't load offers")
	}

	results := make([]Offer, len(horizonOffers.Embedded.Records))
	for i, o := range horizonOffers.Embedded.Records {
		results[i] = Offer(o)
	}
	return results, nil
}

// ManageOffer lets you trade on the DEX. See the Create/Update/DeleteOffer methods below
// to see how this is used.
func (ms *MicroStellar) ManageOffer(sourceSeed string, params *OfferParams, options ...*Options) error {
	if !ValidAddressOrSeed(sourceSeed) {
		return errors.Errorf("invalid source address or seed: %s", sourceSeed)
	}

	if err := params.BuyAsset.Validate(); err != nil {
		return errors.Wrap(err, "ManageOffer")
	}

	if err := params.SellAsset.Validate(); err != nil {
		return errors.Wrap(err, "ManageOffer")
	}

	rate := build.Rate{
		Selling: params.SellAsset.ToStellarAsset(),
		Buying:  params.BuyAsset.ToStellarAsset(),
		Price:   build.Price(params.Price),
	}

	var offerID uint64
	if params.OfferID != "" {
		var err error
		if offerID, err = strconv.ParseUint(params.OfferID, 10, 64); err != nil {
			return errors.Wrapf(err, "ManageOffer: bad OfferID: %v", params.OfferID)
		}
	}

	var builder build.ManageOfferBuilder
	switch params.OfferType {
	case OfferCreate:
		amount := build.Amount(params.SellAmount)
		builder = build.CreateOffer(rate, amount)
	case OfferCreatePassive:
		amount := build.Amount(params.SellAmount)
		builder = build.CreatePassiveOffer(rate, amount)
	case OfferUpdate:
		amount := build.Amount(params.SellAmount)
		builder = build.UpdateOffer(rate, amount, build.OfferID(offerID))
	case OfferDelete:
		builder = build.DeleteOffer(rate, build.OfferID(offerID))
	default:
		return errors.Errorf("ManageOffer: bad OfferType: %v", params.OfferType)
	}

	tx := NewTx(ms.networkName, ms.params)

	if len(options) > 0 {
		tx.SetOptions(options[0])
	}

	tx.Build(sourceAccount(sourceSeed), builder)
	tx.Sign(sourceSeed)
	tx.Submit()
	return tx.Err()
}

// CreateOffer creates an offer to trade sellAmount of sellAsset held by sourceSeed for buyAsset at
// price (which buy_unit-over-sell_unit.)  The offer is made on Stellar's decentralized exchange (DEX.)
//
// You can use add Opts().MakePassive() to make this a passive offer.
func (ms *MicroStellar) CreateOffer(sourceSeed string, sellAsset *Asset, buyAsset *Asset, price string, sellAmount string, options ...*Options) error {
	offerType := OfferCreate

	if len(options) > 0 {
		if options[0].passiveOffer {
			offerType = OfferCreatePassive
		}
	}

	return ms.ManageOffer(sourceSeed, &OfferParams{
		OfferType:  offerType,
		SellAsset:  sellAsset,
		SellAmount: sellAmount,
		BuyAsset:   buyAsset,
		Price:      price,
	}, options...)
}

// UpdateOffer updates the existing offer with ID offerID on the DEX.
func (ms *MicroStellar) UpdateOffer(sourceSeed string, offerID string, sellAsset *Asset, buyAsset *Asset, price string, sellAmount string, options ...*Options) error {
	return ms.ManageOffer(sourceSeed, &OfferParams{
		OfferType:  OfferUpdate,
		SellAsset:  sellAsset,
		SellAmount: sellAmount,
		BuyAsset:   buyAsset,
		Price:      price,
		OfferID:    offerID,
	}, options...)
}

// DeleteOffer deletes the specified parameters (assets, price, ID) on the DEX.
func (ms *MicroStellar) DeleteOffer(sourceSeed string, offerID string, sellAsset *Asset, buyAsset *Asset, price string, options ...*Options) error {
	return ms.ManageOffer(sourceSeed, &OfferParams{
		OfferType: OfferDelete,
		SellAsset: sellAsset,
		BuyAsset:  buyAsset,
		Price:     price,
		OfferID:   offerID,
	}, options...)
}

// FindPaths finds payment paths between source and dest assets.
func (ms *MicroStellar) FindPaths(sourceAddress string, destAddress string, destAsset *Asset, destAmount string, options ...*Options) ([]Path, error) {
	tx := NewTx(ms.networkName, ms.params)
	client := tx.GetClient()
	baseURL := strings.TrimRight(client.URL, "/") + "/paths"

	query := url.Values{}

	query.Add("source_account", sourceAddress)
	query.Add("destination_account", destAddress)

	query.Add("destination_asset_type", string(destAsset.Type))
	query.Add("destination_asset_code", destAsset.Code)
	query.Add("destination_asset_issuer", destAsset.Issuer)
	query.Add("destination_amount", destAmount)

	endpoint := fmt.Sprintf(baseURL+"?%s", query.Encode())
	if _, err := url.Parse(endpoint); err != nil {
		return nil, errors.Errorf("endpoint parse error: %v", err)
	}

	debugf("FindPaths", "querying endpoint: %s", endpoint)
	resp, err := client.HTTP.Get(endpoint)
	if err != nil {
		return nil, errors.Errorf("failed to query server: %v", err)
	}

	var pathResponse PathResponse
	bytes, _ := ioutil.ReadAll(resp.Body)
	body := string(bytes)

	debugf("FindPaths", "Got Body: %+v", body)
	// json.NewDecoder(resp.Body).Decode(&path)

	json.Unmarshal(bytes, &pathResponse)
	return pathResponse.Embedded.Records, nil
}
