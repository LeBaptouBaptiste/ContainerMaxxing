// Package source produit l'état de la chaîne de déploiement.
// Chaque implémentation est interrogée périodiquement par le hub (polling
// côté serveur) : le navigateur n'appelle jamais GitHub ni Azure directement,
// donc aucun jeton n'a besoin d'être exposé au frontend.
package source

import (
	"context"

	"github.com/LeBaptouBaptiste/ContainerMaxxing/internal/model"
)

type Source interface {
	Poll(ctx context.Context) (model.Snapshot, error)
}
