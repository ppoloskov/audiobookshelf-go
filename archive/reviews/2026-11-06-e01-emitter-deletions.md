# Strict emitter guard deletion sites

Exactly 17 nil-only guards whose body is one direct emit expression statement, with identifier/literal arguments already computed. Each 3-line guard can become one nil-safe method call, removing 34 physical LOC before method/type costs. The nested guards around `ItemJSON` are excluded.

- `go-backend/internal/adminops/handler.go` — `changedItem` (682-691); guard 687-689, call 688; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/authors.go` — `authorMerge` (605-695); guard 681-683, call 682; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/authors.go` — `authorMerge` (605-695); guard 690-692, call 691; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/authors.go` — `authorDelete` (724-757); guard 752-754, call 753; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/authors.go` — `authorUploadImage` (862-924); guard 920-922, call 921; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/authors.go` — `authorDeleteImage` (951-986); guard 982-984, call 983; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/authors.go` — `authorMatch` (991-1065); guard 1050-1052, call 1051; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/batch_chapters.go` — `updateTracks` (402-488); guard 484-486, call 485; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/library_issues.go` — `closeFeeds` (98-112); guard 108-110, call 109; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/library_writes.go` — `createLibrary` (39-208); guard 204-206, call 205; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/catalog/library_writes.go` — `updateLibrary` (221-492); guard 488-490, call 489; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/lists/collection_http.go` — `createCollection` (19-105); guard 101-103, call 102; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/lists/collection_http.go` — `addCollectionBook` (413-478); guard 473-475, call 474; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/lists/handler.go` — `collectionID` (112-165); guard 160-162, call 161; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/notifications/handler.go` — `deliverActive` (392-427); guard 423-425, call 424; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/sharing/feeds.go` — `openFeed` (250-315); guard 311-313, call 312; 3 guard LOC; saves 2 LOC.
- `go-backend/internal/userapi/userapi.go` — `createUser` (519-625); guard 621-623, call 622; 3 guard LOC; saves 2 LOC.
