package cmd

import (
	"context"
	"os"
	"strings"

	"github.com/steipete/gogcli/internal/googleapi"
	"github.com/steipete/gogcli/internal/outfmt"
	"github.com/steipete/gogcli/internal/ui"
)

type MapsCmd struct {
	Places MapsPlacesCmd `cmd:"" name:"places" aliases:"place" help:"Google Maps Places API"`
}

type MapsPlacesCmd struct {
	Search  MapsPlacesSearchCmd  `cmd:"" name:"search" aliases:"find" help:"Search Places by text"`
	Details MapsPlacesDetailsCmd `cmd:"" name:"details" aliases:"get,info,show" help:"Get Place details"`
}

type MapsPlacesSearchCmd struct {
	Query    []string `arg:"" name:"query" help:"Text search query"`
	Language string   `name:"language" help:"BCP-47 language code"`
	Region   string   `name:"region" help:"CLDR region code"`
}

func (c *MapsPlacesSearchCmd) Run(ctx context.Context, flags *RootFlags) error {
	query := strings.TrimSpace(strings.Join(c.Query, " "))
	if query == "" {
		return usage("missing query")
	}
	client, err := newMapsPlacesClient()
	if err != nil {
		return err
	}
	opts := googleapi.PlacesLookupOptions{
		LanguageCode: strings.TrimSpace(c.Language),
		RegionCode:   strings.TrimSpace(c.Region),
	}
	place, err := client.TextSearch(ctx, query, opts)
	if err != nil {
		return err
	}
	return writeMapsPlace(ctx, place)
}

type MapsPlacesDetailsCmd struct {
	PlaceID  string `arg:"" name:"placeId" help:"Place ID (places/{id} accepted)"`
	Language string `name:"language" help:"BCP-47 language code"`
	Region   string `name:"region" help:"CLDR region code"`
}

func (c *MapsPlacesDetailsCmd) Run(ctx context.Context, flags *RootFlags) error {
	placeID := strings.TrimSpace(c.PlaceID)
	if placeID == "" {
		return usage("empty placeId")
	}
	client, err := newMapsPlacesClient()
	if err != nil {
		return err
	}
	opts := googleapi.PlacesLookupOptions{
		LanguageCode: strings.TrimSpace(c.Language),
		RegionCode:   strings.TrimSpace(c.Region),
	}
	place, err := client.Details(ctx, placeID, opts)
	if err != nil {
		return err
	}
	return writeMapsPlace(ctx, place)
}

func newMapsPlacesClient() (*googleapi.PlacesClient, error) {
	apiKey, err := placesAPIKey()
	if err != nil {
		return nil, err
	}
	return googleapi.NewPlacesClient(apiKey, googleapi.WithPlacesBaseURL(os.Getenv("GOG_PLACES_BASE_URL"))), nil
}

func writeMapsPlace(ctx context.Context, place *googleapi.Place) error {
	u := ui.FromContext(ctx)
	if outfmt.IsJSON(ctx) {
		return outfmt.WriteJSON(ctx, os.Stdout, map[string]any{"place": place})
	}
	if place == nil {
		u.Err().Println("No place")
		return nil
	}
	u.Out().Printf("id\t%s", place.ID)
	if strings.TrimSpace(place.Name) != "" {
		u.Out().Printf("name\t%s", place.Name)
	}
	if strings.TrimSpace(place.FormattedAddress) != "" {
		u.Out().Printf("address\t%s", place.FormattedAddress)
	}
	if strings.TrimSpace(place.GoogleMapsURI) != "" {
		u.Out().Printf("maps_uri\t%s", place.GoogleMapsURI)
	}
	return nil
}
