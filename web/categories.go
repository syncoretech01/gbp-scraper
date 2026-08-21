package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
)

var (
	// ErrCategoryGroupsUnsupported indicates the repository cannot store
	// reusable category groups.
	ErrCategoryGroupsUnsupported = errors.New("category groups are unavailable")
	// ErrCategoryGroupNotFound indicates the requested group does not exist.
	ErrCategoryGroupNotFound = errors.New("category group not found")
	// ErrInvalidCategoryGroup indicates a rejected category-group save.
	ErrInvalidCategoryGroup = errors.New("invalid category group")
)

const (
	// MaximumCategoryGroupNameLength bounds a group name so the wizard's
	// picker stays readable.
	MaximumCategoryGroupNameLength = 64
	// MaximumCategoryGroupEntries bounds how many categories one group holds.
	MaximumCategoryGroupEntries = 200
	// MaximumCategoryGroups bounds how many groups the workspace stores.
	MaximumCategoryGroups = 100
	// categoryGroupsSettingKey is the settings row the groups are stored in.
	// Using the existing settings table keeps this additive: no schema change
	// is needed and an older database gains the feature on first save.
	categoryGroupsSettingKey = "wizard.category_groups"
	// maximumCategoryGroupBytes bounds the stored JSON blob.
	maximumCategoryGroupBytes = 64 << 10
)

// BusinessCategory is one entry of the bundled Google Maps category
// vocabulary. The list is embedded in the binary: it needs no network call,
// no paid taxonomy service, and no dataset file on disk.
type BusinessCategory struct {
	// Name is the query term an operator would actually type into Maps.
	Name string `json:"name"`
	// Sector groups related categories in the picker.
	Sector string `json:"sector"`
}

// CategoryGroup is a reusable, named set of business categories.
type CategoryGroup struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Categories []string   `json:"categories"`
	UseCount   int        `json:"use_count"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
}

// businessCategories is the bundled vocabulary, ordered by sector then name.
// Every entry is a category label Google Maps actually uses as a listing
// category, so pairing one with a location produces a realistic Maps query.
var businessCategories = []BusinessCategory{
	{Name: "Accountant", Sector: "Professional services"},
	{Name: "Advertising agency", Sector: "Professional services"},
	{Name: "Architect", Sector: "Professional services"},
	{Name: "Business consultant", Sector: "Professional services"},
	{Name: "Employment agency", Sector: "Professional services"},
	{Name: "Engineering consultant", Sector: "Professional services"},
	{Name: "Financial advisor", Sector: "Professional services"},
	{Name: "Insurance agency", Sector: "Professional services"},
	{Name: "Law firm", Sector: "Professional services"},
	{Name: "Marketing agency", Sector: "Professional services"},
	{Name: "Notary public", Sector: "Professional services"},
	{Name: "Printing service", Sector: "Professional services"},
	{Name: "Real estate agency", Sector: "Professional services"},
	{Name: "Recruiter", Sector: "Professional services"},
	{Name: "Tax preparation service", Sector: "Professional services"},
	{Name: "Translator", Sector: "Professional services"},
	{Name: "Web designer", Sector: "Professional services"},

	{Name: "Air conditioning contractor", Sector: "Home and trade services"},
	{Name: "Appliance repair service", Sector: "Home and trade services"},
	{Name: "Carpenter", Sector: "Home and trade services"},
	{Name: "Carpet cleaning service", Sector: "Home and trade services"},
	{Name: "Cleaning service", Sector: "Home and trade services"},
	{Name: "Electrician", Sector: "Home and trade services"},
	{Name: "Fencing contractor", Sector: "Home and trade services"},
	{Name: "Flooring contractor", Sector: "Home and trade services"},
	{Name: "Garage door supplier", Sector: "Home and trade services"},
	{Name: "General contractor", Sector: "Home and trade services"},
	{Name: "Handyman", Sector: "Home and trade services"},
	{Name: "Heating contractor", Sector: "Home and trade services"},
	{Name: "Interior designer", Sector: "Home and trade services"},
	{Name: "Landscaper", Sector: "Home and trade services"},
	{Name: "Locksmith", Sector: "Home and trade services"},
	{Name: "Moving company", Sector: "Home and trade services"},
	{Name: "Painter", Sector: "Home and trade services"},
	{Name: "Pest control service", Sector: "Home and trade services"},
	{Name: "Plumber", Sector: "Home and trade services"},
	{Name: "Pool cleaning service", Sector: "Home and trade services"},
	{Name: "Roofing contractor", Sector: "Home and trade services"},
	{Name: "Security system installer", Sector: "Home and trade services"},
	{Name: "Solar energy contractor", Sector: "Home and trade services"},
	{Name: "Tree service", Sector: "Home and trade services"},
	{Name: "Window installation service", Sector: "Home and trade services"},

	{Name: "Chiropractor", Sector: "Health and medical"},
	{Name: "Dental clinic", Sector: "Health and medical"},
	{Name: "Dentist", Sector: "Health and medical"},
	{Name: "Dermatologist", Sector: "Health and medical"},
	{Name: "Doctor", Sector: "Health and medical"},
	{Name: "Family practice physician", Sector: "Health and medical"},
	{Name: "Home health care service", Sector: "Health and medical"},
	{Name: "Medical clinic", Sector: "Health and medical"},
	{Name: "Mental health counselor", Sector: "Health and medical"},
	{Name: "Optometrist", Sector: "Health and medical"},
	{Name: "Orthodontist", Sector: "Health and medical"},
	{Name: "Pediatrician", Sector: "Health and medical"},
	{Name: "Pharmacy", Sector: "Health and medical"},
	{Name: "Physical therapist", Sector: "Health and medical"},
	{Name: "Podiatrist", Sector: "Health and medical"},
	{Name: "Urgent care center", Sector: "Health and medical"},
	{Name: "Veterinarian", Sector: "Health and medical"},

	{Name: "Bakery", Sector: "Food and drink"},
	{Name: "Bar", Sector: "Food and drink"},
	{Name: "Brewery", Sector: "Food and drink"},
	{Name: "Cafe", Sector: "Food and drink"},
	{Name: "Caterer", Sector: "Food and drink"},
	{Name: "Coffee shop", Sector: "Food and drink"},
	{Name: "Deli", Sector: "Food and drink"},
	{Name: "Food truck", Sector: "Food and drink"},
	{Name: "Ice cream shop", Sector: "Food and drink"},
	{Name: "Juice shop", Sector: "Food and drink"},
	{Name: "Pizza restaurant", Sector: "Food and drink"},
	{Name: "Restaurant", Sector: "Food and drink"},
	{Name: "Wine bar", Sector: "Food and drink"},

	{Name: "Barber shop", Sector: "Beauty and wellness"},
	{Name: "Beauty salon", Sector: "Beauty and wellness"},
	{Name: "Day spa", Sector: "Beauty and wellness"},
	{Name: "Gym", Sector: "Beauty and wellness"},
	{Name: "Hair salon", Sector: "Beauty and wellness"},
	{Name: "Massage therapist", Sector: "Beauty and wellness"},
	{Name: "Nail salon", Sector: "Beauty and wellness"},
	{Name: "Personal trainer", Sector: "Beauty and wellness"},
	{Name: "Pilates studio", Sector: "Beauty and wellness"},
	{Name: "Tattoo shop", Sector: "Beauty and wellness"},
	{Name: "Yoga studio", Sector: "Beauty and wellness"},

	{Name: "Auto body shop", Sector: "Automotive"},
	{Name: "Auto glass shop", Sector: "Automotive"},
	{Name: "Auto parts store", Sector: "Automotive"},
	{Name: "Auto repair shop", Sector: "Automotive"},
	{Name: "Car dealer", Sector: "Automotive"},
	{Name: "Car detailing service", Sector: "Automotive"},
	{Name: "Car wash", Sector: "Automotive"},
	{Name: "Motorcycle dealer", Sector: "Automotive"},
	{Name: "Tire shop", Sector: "Automotive"},
	{Name: "Towing service", Sector: "Automotive"},
	{Name: "Transmission shop", Sector: "Automotive"},

	{Name: "Bicycle shop", Sector: "Retail"},
	{Name: "Book store", Sector: "Retail"},
	{Name: "Clothing store", Sector: "Retail"},
	{Name: "Convenience store", Sector: "Retail"},
	{Name: "Electronics store", Sector: "Retail"},
	{Name: "Florist", Sector: "Retail"},
	{Name: "Furniture store", Sector: "Retail"},
	{Name: "Garden center", Sector: "Retail"},
	{Name: "Gift shop", Sector: "Retail"},
	{Name: "Grocery store", Sector: "Retail"},
	{Name: "Hardware store", Sector: "Retail"},
	{Name: "Jewelry store", Sector: "Retail"},
	{Name: "Liquor store", Sector: "Retail"},
	{Name: "Pet store", Sector: "Retail"},
	{Name: "Shoe store", Sector: "Retail"},
	{Name: "Sporting goods store", Sector: "Retail"},
	{Name: "Thrift store", Sector: "Retail"},

	{Name: "Child care service", Sector: "Education and childcare"},
	{Name: "Driving school", Sector: "Education and childcare"},
	{Name: "Language school", Sector: "Education and childcare"},
	{Name: "Music school", Sector: "Education and childcare"},
	{Name: "Preschool", Sector: "Education and childcare"},
	{Name: "Tutoring service", Sector: "Education and childcare"},

	{Name: "Bed and breakfast", Sector: "Hospitality and events"},
	{Name: "Event venue", Sector: "Hospitality and events"},
	{Name: "Hotel", Sector: "Hospitality and events"},
	{Name: "Party equipment rental service", Sector: "Hospitality and events"},
	{Name: "Photographer", Sector: "Hospitality and events"},
	{Name: "Travel agency", Sector: "Hospitality and events"},
	{Name: "Wedding planner", Sector: "Hospitality and events"},
}

// BusinessCategories returns the bundled vocabulary in picker order.
func BusinessCategories() []BusinessCategory {
	categories := make([]BusinessCategory, len(businessCategories))
	copy(categories, businessCategories)

	return categories
}

// BusinessCategorySectors returns the distinct sectors in picker order.
func BusinessCategorySectors() []string {
	seen := make(map[string]struct{}, len(businessCategories))
	sectors := make([]string, 0, 10)
	for _, category := range businessCategories {
		if _, ok := seen[category.Sector]; ok {
			continue
		}
		seen[category.Sector] = struct{}{}
		sectors = append(sectors, category.Sector)
	}

	return sectors
}

// KnownBusinessCategory reports whether a name is part of the bundled
// vocabulary, compared case-insensitively.
func KnownBusinessCategory(name string) bool {
	needle := strings.ToLower(strings.TrimSpace(name))
	for _, category := range businessCategories {
		if strings.ToLower(category.Name) == needle {
			return true
		}
	}

	return false
}

// SupportsCategoryGroups reports whether reusable groups can be stored.
func (s *Service) SupportsCategoryGroups() bool {
	_, ok := s.repo.(settingsRepository)

	return ok
}

// ListCategoryGroups returns saved groups in name order.
func (s *Service) ListCategoryGroups(ctx context.Context) ([]CategoryGroup, error) {
	groups, err := s.loadCategoryGroups(ctx)
	if err != nil {
		return nil, err
	}

	return groups, nil
}

// SaveCategoryGroup creates or replaces one reusable group. A group whose
// name already exists is replaced, so saving twice never duplicates.
func (s *Service) SaveCategoryGroup(ctx context.Context, group CategoryGroup) (CategoryGroup, error) {
	normalized, err := normalizeCategoryGroup(group)
	if err != nil {
		return CategoryGroup{}, err
	}

	groups, err := s.loadCategoryGroups(ctx)
	if err != nil {
		return CategoryGroup{}, err
	}

	now := time.Now().UTC()
	normalized.UpdatedAt = now
	replaced := false
	for index := range groups {
		sameID := normalized.ID != "" && groups[index].ID == normalized.ID
		sameName := strings.EqualFold(groups[index].Name, normalized.Name)
		if !sameID && !sameName {
			continue
		}
		normalized.ID = groups[index].ID
		normalized.CreatedAt = groups[index].CreatedAt
		normalized.UseCount = groups[index].UseCount
		normalized.LastUsedAt = groups[index].LastUsedAt
		groups[index] = normalized
		replaced = true

		break
	}
	if !replaced {
		if len(groups) >= MaximumCategoryGroups {
			return CategoryGroup{}, fmt.Errorf("%w: at most %d groups can be stored",
				ErrInvalidCategoryGroup, MaximumCategoryGroups)
		}
		if normalized.ID == "" {
			normalized.ID = uuid.NewString()
		}
		normalized.CreatedAt = now
		groups = append(groups, normalized)
	}

	if err := s.storeCategoryGroups(ctx, groups); err != nil {
		return CategoryGroup{}, err
	}

	return normalized, nil
}

// DeleteCategoryGroup removes one reusable group.
func (s *Service) DeleteCategoryGroup(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return ErrCategoryGroupNotFound
	}

	groups, err := s.loadCategoryGroups(ctx)
	if err != nil {
		return err
	}

	kept := make([]CategoryGroup, 0, len(groups))
	for _, group := range groups {
		if group.ID == id {
			continue
		}
		kept = append(kept, group)
	}
	if len(kept) == len(groups) {
		return ErrCategoryGroupNotFound
	}

	return s.storeCategoryGroups(ctx, kept)
}

// TouchCategoryGroupUse records that a group was inserted into a wizard.
func (s *Service) TouchCategoryGroupUse(ctx context.Context, id string, now time.Time) (CategoryGroup, error) {
	id = strings.TrimSpace(id)
	groups, err := s.loadCategoryGroups(ctx)
	if err != nil {
		return CategoryGroup{}, err
	}

	for index := range groups {
		if groups[index].ID != id {
			continue
		}
		used := now.UTC()
		groups[index].UseCount++
		groups[index].LastUsedAt = &used
		groups[index].UpdatedAt = used
		if err := s.storeCategoryGroups(ctx, groups); err != nil {
			return CategoryGroup{}, err
		}

		return groups[index], nil
	}

	return CategoryGroup{}, ErrCategoryGroupNotFound
}

func (s *Service) loadCategoryGroups(ctx context.Context) ([]CategoryGroup, error) {
	values, err := s.LoadSettings(ctx)
	if err != nil {
		if errors.Is(err, ErrSettingsUnsupported) {
			return nil, ErrCategoryGroupsUnsupported
		}

		return nil, fmt.Errorf("load category groups: %w", err)
	}

	raw := strings.TrimSpace(values[categoryGroupsSettingKey])
	if raw == "" {
		return []CategoryGroup{}, nil
	}

	var groups []CategoryGroup
	if err := json.Unmarshal([]byte(raw), &groups); err != nil {
		// A corrupt blob must not brick the wizard; an empty list is the
		// safe reading and the next save rewrites it.
		return []CategoryGroup{}, nil
	}
	sort.SliceStable(groups, func(left, right int) bool {
		return strings.ToLower(groups[left].Name) < strings.ToLower(groups[right].Name)
	})

	return groups, nil
}

func (s *Service) storeCategoryGroups(ctx context.Context, groups []CategoryGroup) error {
	encoded, err := json.Marshal(groups)
	if err != nil {
		return fmt.Errorf("encode category groups: %w", err)
	}
	if len(encoded) > maximumCategoryGroupBytes {
		return fmt.Errorf("%w: stored groups exceed %d bytes", ErrInvalidCategoryGroup, maximumCategoryGroupBytes)
	}
	if err := s.SaveSettings(ctx, map[string]string{categoryGroupsSettingKey: string(encoded)}); err != nil {
		if errors.Is(err, ErrSettingsUnsupported) {
			return ErrCategoryGroupsUnsupported
		}

		return fmt.Errorf("save category groups: %w", err)
	}

	return nil
}

func normalizeCategoryGroup(group CategoryGroup) (CategoryGroup, error) {
	name := strings.TrimSpace(group.Name)
	if name == "" || len([]rune(name)) > MaximumCategoryGroupNameLength {
		return CategoryGroup{}, fmt.Errorf("%w: name must be 1 to %d characters",
			ErrInvalidCategoryGroup, MaximumCategoryGroupNameLength)
	}
	if strings.ContainsFunc(name, unicode.IsControl) {
		return CategoryGroup{}, fmt.Errorf("%w: name contains control characters", ErrInvalidCategoryGroup)
	}

	categories := normalizeJobFilterList(group.Categories)
	if len(categories) == 0 {
		return CategoryGroup{}, fmt.Errorf("%w: at least one category is required", ErrInvalidCategoryGroup)
	}
	if len(categories) > MaximumCategoryGroupEntries {
		return CategoryGroup{}, fmt.Errorf("%w: at most %d categories per group",
			ErrInvalidCategoryGroup, MaximumCategoryGroupEntries)
	}
	for _, category := range categories {
		if len([]rune(category)) > maximumJobFilterCategoryLen {
			return CategoryGroup{}, fmt.Errorf("%w: a category must be at most %d characters",
				ErrInvalidCategoryGroup, maximumJobFilterCategoryLen)
		}
		if strings.ContainsFunc(category, unicode.IsControl) {
			return CategoryGroup{}, fmt.Errorf("%w: a category contains control characters", ErrInvalidCategoryGroup)
		}
	}

	id := strings.TrimSpace(group.ID)
	if id != "" && !validMapEntityID(id) {
		return CategoryGroup{}, fmt.Errorf("%w: invalid group ID", ErrInvalidCategoryGroup)
	}

	return CategoryGroup{ID: id, Name: name, Categories: categories}, nil
}
