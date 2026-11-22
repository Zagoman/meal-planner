package webfake

import (
	"context"

	"github.com/google/uuid"

	"github.com/Zagoman/meal-planner/internal/domain"
)

type recipe struct {
	Title    string
	Protein  domain.Protein
	Minutes  int
	OnePot   bool
	Ings     [][4]string // name, normalized name, unit, section
	Qty      []float64
	Steps    []domain.Step
	AfterBay []string
}

func timer(n int) *int { return &n }

// recipes is the fixed rotation the fake plans draw from.
var recipes = []recipe{
	{
		Title: "One-pot chicken and rice", Protein: domain.ProteinChicken, Minutes: 35, OnePot: true,
		Ings: [][4]string{
			{"chicken thighs", "chicken thigh", "g", "meat_fish"},
			{"onions, chopped", "onion", "pcs", "produce"},
			{"long-grain rice", "rice", "g", "dry_goods"},
			{"stock cube", "stock cube", "pcs", "spices"},
		},
		Qty: []float64{500, 2, 200, 1},
		Steps: []domain.Step{
			{Text: "Brown the chicken and onions in a large pot.", TimerMin: timer(8)},
			{Text: "Add the rice and 500 ml water, cover and simmer.", TimerMin: timer(18)},
			{Text: "Take out the baby's portion now.", BabyPortion: true},
			{Text: "Crumble in the stock cube, season with salt and serve."},
		},
		AfterBay: []string{"stock cube", "salt"},
	},
	{
		Title: "Salmon, potatoes and green beans", Protein: domain.ProteinFish, Minutes: 30,
		Ings: [][4]string{
			{"salmon fillets", "salmon fillet", "pcs", "meat_fish"},
			{"new potatoes", "potato", "g", "produce"},
			{"green beans", "green bean", "g", "produce"},
			{"lemon", "lemon", "pcs", "produce"},
		},
		Qty: []float64{2, 600, 200, 1},
		Steps: []domain.Step{
			{Text: "Boil the potatoes until tender.", TimerMin: timer(15)},
			{Text: "Roast the salmon at 200 °C.", TimerMin: timer(12)},
			{Text: "Steam the green beans.", TimerMin: timer(5)},
			{Text: "Flake some salmon with potato and beans for the baby.", BabyPortion: true},
			{Text: "Season with salt and lemon and serve."},
		},
		AfterBay: []string{"salt", "lemon"},
	},
	{
		Title: "Lentil and sweet potato dal", Protein: domain.ProteinVegetarian, Minutes: 40, OnePot: true,
		Ings: [][4]string{
			{"red lentils", "red lentil", "g", "dry_goods"},
			{"sweet potato", "sweet potato", "pcs", "produce"},
			{"onions, chopped", "onion", "pcs", "produce"},
			{"coconut milk", "coconut milk", "tin", "tins_jars"},
			{"curry powder", "curry powder", "tbsp", "spices"},
		},
		Qty: []float64{250, 1, 1, 1, 1},
		Steps: []domain.Step{
			{Text: "Soften the onion in a pot.", TimerMin: timer(5)},
			{Text: "Add lentils, diced sweet potato, coconut milk and 400 ml water; simmer.", TimerMin: timer(25)},
			{Text: "Mash a portion for the baby.", BabyPortion: true},
			{Text: "Stir in the curry powder and salt, simmer 2 more minutes."},
		},
		AfterBay: []string{"curry powder", "salt"},
	},
	{
		Title: "Pasta with tomato and spinach", Protein: domain.ProteinVegetarian, Minutes: 20,
		Ings: [][4]string{
			{"pasta", "pasta", "g", "dry_goods"},
			{"chopped tomatoes", "chopped tomatoes", "tin", "tins_jars"},
			{"baby spinach", "spinach", "g", "produce"},
			{"parmesan", "parmesan", "g", "dairy_eggs"},
		},
		Qty: []float64{250, 1, 100, 40},
		Steps: []domain.Step{
			{Text: "Cook the pasta.", TimerMin: timer(10)},
			{Text: "Simmer the tomatoes and wilt in the spinach.", TimerMin: timer(8)},
			{Text: "Mix some pasta and sauce for the baby.", BabyPortion: true},
			{Text: "Season with salt and grate over the parmesan."},
		},
		AfterBay: []string{"salt", "parmesan"},
	},
	{
		Title: "Chicken traybake with peppers", Protein: domain.ProteinChicken, Minutes: 45,
		Ings: [][4]string{
			{"chicken thighs", "chicken thigh", "g", "meat_fish"},
			{"red peppers", "red pepper", "pcs", "produce"},
			{"new potatoes", "potato", "g", "produce"},
			{"smoked paprika", "smoked paprika", "tsp", "spices"},
		},
		Qty: []float64{600, 2, 500, 1},
		Steps: []domain.Step{
			{Text: "Toss everything except the paprika with oil on a tray.", TimerMin: nil},
			{Text: "Roast at 200 °C.", TimerMin: timer(35)},
			{Text: "Take out the baby's portion.", BabyPortion: true},
			{Text: "Sprinkle the rest with paprika and salt, roast 5 more minutes.", TimerMin: timer(5)},
		},
		AfterBay: []string{"smoked paprika", "salt"},
	},
	{
		Title: "Cod fish cakes with peas", Protein: domain.ProteinFish, Minutes: 35,
		Ings: [][4]string{
			{"cod fillets", "cod fillet", "g", "meat_fish"},
			{"floury potatoes", "potato", "g", "produce"},
			{"frozen peas", "peas", "g", "frozen"},
			{"eggs", "egg", "pcs", "dairy_eggs"},
		},
		Qty: []float64{300, 400, 200, 1},
		Steps: []domain.Step{
			{Text: "Boil and mash the potatoes.", TimerMin: timer(15)},
			{Text: "Poach the cod and flake it into the mash with the egg.", TimerMin: timer(6)},
			{Text: "Shape small cakes for the baby first.", BabyPortion: true},
			{Text: "Season the rest with salt, shape and fry.", TimerMin: timer(8)},
		},
		AfterBay: []string{"salt"},
	},
	{
		Title: "Bean chilli", Protein: domain.ProteinVegetarian, Minutes: 40, OnePot: true,
		Ings: [][4]string{
			{"kidney beans", "kidney beans", "tin", "tins_jars"},
			{"chopped tomatoes", "chopped tomatoes", "tin", "tins_jars"},
			{"onions, chopped", "onion", "pcs", "produce"},
			{"chilli powder", "chilli powder", "tsp", "spices"},
			{"tortillas", "tortilla", "pcs", "bakery"},
		},
		Qty: []float64{2, 1, 1, 1, 6},
		Steps: []domain.Step{
			{Text: "Soften the onion.", TimerMin: timer(5)},
			{Text: "Add beans and tomatoes and simmer.", TimerMin: timer(20)},
			{Text: "Spoon out the baby's portion.", BabyPortion: true},
			{Text: "Stir in the chilli powder and salt; serve with warm tortillas."},
		},
		AfterBay: []string{"chilli powder", "salt"},
	},
}

func (r recipe) instantiate(day domain.Weekday, servings int) domain.Meal {
	m := domain.Meal{
		ID: uuid.New(), Day: day, Title: r.Title, Protein: r.Protein, Minutes: r.Minutes,
		OnePot: r.OnePot, Servings: servings, AfterBabyPortion: r.AfterBay,
	}
	for i, in := range r.Ings {
		m.Ingredients = append(m.Ingredients, domain.Ingredient{
			ID: uuid.New(), Name: in[0], NormalizedName: in[1], Qty: r.Qty[i], Unit: in[2], Section: in[3],
		})
	}
	m.Steps = append(m.Steps, r.Steps...)
	return m
}

// Seeded is what Seed created, for tests and the e2e server.
type Seeded struct {
	Home, Neighbours   uuid.UUID
	Anna, Ben, Carl    domain.Scope // Anna and Ben in Home (Anna admin), Carl admin of Neighbours
	AnnaInNeighbours   domain.Scope
	HomePlan           domain.Plan
	NeighboursPlan     domain.Plan
	FirstHomeItem      domain.ListItem
	FirstNeighbourItem domain.ListItem
}

// SeedPassword is the password of every seeded member (test data only).
const SeedPassword = "correct horse"

// Seed creates two households with members, an active plan and a list each.
func (s *Store) Seed() Seeded {
	var out Seeded
	out.Home = s.AddHousehold("Home")
	out.Neighbours = s.AddHousehold("Neighbours")
	s.households[out.Neighbours].next = 3 // a different first week
	out.Anna = s.AddMember(out.Home, "anna@example.test", "Anna", SeedPassword, domain.RoleAdmin)
	out.Ben = s.AddMember(out.Home, "ben@example.test", "Ben", SeedPassword, domain.RoleMember)
	out.Carl = s.AddMember(out.Neighbours, "carl@example.test", "Carl", SeedPassword, domain.RoleAdmin)
	out.AnnaInNeighbours = s.AddMember(out.Neighbours, "anna@example.test", "Anna", SeedPassword, domain.RoleMember)
	_, _ = s.PutPantry(context.Background(), out.Anna, []string{"rice", "curry powder"})

	delay := s.GenerateDelay
	s.GenerateDelay = 0
	out.HomePlan, _ = s.Generate(context.Background(), out.Anna, "quick dinners")
	out.NeighboursPlan, _ = s.Generate(context.Background(), out.Carl, "anything")
	s.GenerateDelay = delay

	hl, _ := s.Active(context.Background(), out.Anna)
	out.FirstHomeItem = hl.Sections[0].Items[0]
	nl, _ := s.Active(context.Background(), out.Carl)
	out.FirstNeighbourItem = nl.Sections[0].Items[0]
	return out
}
