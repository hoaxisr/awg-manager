package proxyrt

import (
	"context"
	"testing"
)

// Второй прогон реконсиляции обязан быть пустым. Свойство ловит класс ошибок,
// который иначе виден только на железе.
func TestReconcileIsIdempotent(t *testing.T) {
	cases := []struct {
		name string
		res  []Resource
	}{
		{"один ресурс с нуля", []Resource{
			&statefulResource{id: "a", want: "up"},
		}},
		{"цепочка из трёх", []Resource{
			&statefulResource{id: "a", want: "created"},
			&statefulResource{id: "b", want: "10.70.0.5"},
			&statefulResource{id: "c", want: "up"},
		}},
		{"часть уже в нужном состоянии", []Resource{
			&statefulResource{id: "a", want: "created", current: "created"},
			&statefulResource{id: "b", want: "up"},
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := NewReconciler(staticRole{res: c.res}, nil, ReconcileOpts{})

			if _, phase := rec.Run(context.Background(), IntentEnabled); phase != PhaseSettled {
				t.Fatalf("первый прогон: фаза %q, ожидали settled", phase)
			}

			second, phase := rec.Run(context.Background(), IntentEnabled)
			if len(second.Steps) != 0 {
				t.Fatalf("второй прогон дал шаги %v — реконсиляция не идемпотентна", second.Steps)
			}
			if phase != PhaseSettled {
				t.Fatalf("второй прогон: фаза %q, ожидали settled", phase)
			}
			if second.Passes != 1 {
				t.Fatalf("второй прогон занял %d проходов, ожидали 1", second.Passes)
			}
		})
	}
}
