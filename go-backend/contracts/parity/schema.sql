-- Audiobookshelf upstream fresh-init schema; source-executed, do not hand edit.
-- Baseline: edb1f7efc92ecacdef51aafb4d9d5054ce10604f
-- Source SHA-256: {"server/Database.js":"c7a25f2d117862c6f03b0ae8d1d4c4ad35eef95ba61f5874fd9471b64243bc33","server/managers/MigrationManager.js":"212aab0c4be11e67bae8943a49a76fb4088a23546037c5c090cb9f48d8649ccb","server/objects/settings/ServerSettings.js":"7da852d861f6796d8eb7b69e1a6c0d16f191436b61ae213a9e3c517436a1078b","server/objects/settings/EmailSettings.js":"4e993a2ae3aec4222d89ad117e34b6aba54e689e91e3dd7a9adf622202d56be7","server/objects/settings/NotificationSettings.js":"de5cc8663bd781440e2d3de8ac599e812ef9e815d22fe11e6463fb319342ba50"}
-- PRAGMA user_version: 0
-- Migration tables/row counts: migrationsMeta=2
-- Generated from an isolated fresh upstream initialization; fixture data and secrets are excluded.
CREATE TABLE `apiKeys` (`id` UUID PRIMARY KEY, `name` VARCHAR(255) NOT NULL, `description` TEXT, `expiresAt` DATETIME, `lastUsedAt` DATETIME, `isActive` TINYINT(1) NOT NULL DEFAULT 0, `permissions` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `userId` UUID REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE, `createdByUserId` UUID REFERENCES `users` (`id`) ON DELETE SET NULL ON UPDATE CASCADE);

CREATE TABLE `authors` (`id` UUID PRIMARY KEY, `name` VARCHAR(255), `lastFirst` VARCHAR(255), `asin` VARCHAR(255), `description` TEXT, `imagePath` VARCHAR(255), `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `libraryId` UUID REFERENCES `libraries` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `bookAuthors` (`id` UUID PRIMARY KEY, `createdAt` DATETIME NOT NULL, `bookId` UUID REFERENCES `books` (`id`) ON DELETE CASCADE ON UPDATE CASCADE, `authorId` UUID REFERENCES `authors` (`id`) ON DELETE CASCADE ON UPDATE CASCADE, UNIQUE (`bookId`, `authorId`));

CREATE TABLE `bookSeries` (`id` UUID PRIMARY KEY, `sequence` VARCHAR(255), `createdAt` DATETIME NOT NULL, `bookId` UUID REFERENCES `books` (`id`) ON DELETE CASCADE ON UPDATE CASCADE, `seriesId` UUID REFERENCES `series` (`id`) ON DELETE CASCADE ON UPDATE CASCADE, UNIQUE (`bookId`, `seriesId`));

CREATE TABLE `books` (`id` UUID PRIMARY KEY, `title` VARCHAR(255), `titleIgnorePrefix` VARCHAR(255), `subtitle` VARCHAR(255), `publishedYear` VARCHAR(255), `publishedDate` VARCHAR(255), `publisher` VARCHAR(255), `description` TEXT, `isbn` VARCHAR(255), `asin` VARCHAR(255), `language` VARCHAR(255), `explicit` TINYINT(1), `abridged` TINYINT(1), `coverPath` VARCHAR(255), `duration` FLOAT, `narrators` JSON, `audioFiles` JSON, `ebookFile` JSON, `chapters` JSON, `tags` JSON, `genres` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL);

CREATE TABLE `collectionBooks` (`id` UUID PRIMARY KEY, `order` INTEGER, `createdAt` DATETIME NOT NULL, `bookId` UUID REFERENCES `books` (`id`) ON DELETE CASCADE ON UPDATE CASCADE, `collectionId` UUID REFERENCES `collections` (`id`) ON DELETE CASCADE ON UPDATE CASCADE, UNIQUE (`bookId`, `collectionId`));

CREATE TABLE `collections` (`id` UUID PRIMARY KEY, `name` VARCHAR(255), `description` TEXT, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `libraryId` UUID REFERENCES `libraries` (`id`) ON DELETE SET NULL ON UPDATE CASCADE);

CREATE TABLE `customMetadataProviders` (`id` UUID PRIMARY KEY, `name` VARCHAR(255), `mediaType` VARCHAR(255), `url` VARCHAR(255), `authHeaderValue` VARCHAR(255), `extraData` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL);

CREATE TABLE `devices` (`id` UUID PRIMARY KEY, `deviceId` VARCHAR(255), `clientName` VARCHAR(255), `clientVersion` VARCHAR(255), `ipAddress` VARCHAR(255), `deviceName` VARCHAR(255), `deviceVersion` VARCHAR(255), `extraData` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `userId` UUID REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `feedEpisodes` (`id` UUID PRIMARY KEY, `title` VARCHAR(255), `author` VARCHAR(255), `description` TEXT, `siteURL` VARCHAR(255), `enclosureURL` VARCHAR(255), `enclosureType` VARCHAR(255), `enclosureSize` BIGINT, `pubDate` VARCHAR(255), `season` VARCHAR(255), `episode` VARCHAR(255), `episodeType` VARCHAR(255), `duration` FLOAT, `filePath` VARCHAR(255), `explicit` TINYINT(1), `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `feedId` UUID REFERENCES `feeds` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `feeds` (`id` UUID PRIMARY KEY, `slug` VARCHAR(255), `entityType` VARCHAR(255), `entityId` UUID, `entityUpdatedAt` DATETIME, `serverAddress` VARCHAR(255), `feedURL` VARCHAR(255), `imageURL` VARCHAR(255), `siteURL` VARCHAR(255), `title` VARCHAR(255), `description` TEXT, `author` VARCHAR(255), `podcastType` VARCHAR(255), `language` VARCHAR(255), `ownerName` VARCHAR(255), `ownerEmail` VARCHAR(255), `explicit` TINYINT(1), `preventIndexing` TINYINT(1), `coverPath` VARCHAR(255), `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `userId` UUID REFERENCES `users` (`id`) ON DELETE SET NULL ON UPDATE CASCADE);

CREATE TABLE `libraries` (`id` UUID PRIMARY KEY, `name` VARCHAR(255), `displayOrder` INTEGER, `icon` VARCHAR(255), `mediaType` VARCHAR(255), `provider` VARCHAR(255), `lastScan` DATETIME, `lastScanVersion` VARCHAR(255), `settings` JSON, `extraData` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL);

CREATE TABLE `libraryFolders` (`id` UUID PRIMARY KEY, `path` VARCHAR(255), `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `libraryId` UUID REFERENCES `libraries` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `libraryItems` (`id` UUID PRIMARY KEY, `ino` VARCHAR(255), `path` VARCHAR(255), `relPath` VARCHAR(255), `mediaId` UUID, `mediaType` VARCHAR(255), `isFile` TINYINT(1), `isMissing` TINYINT(1), `isInvalid` TINYINT(1), `mtime` DATETIME, `ctime` DATETIME, `birthtime` DATETIME, `size` BIGINT, `lastScan` DATETIME, `lastScanVersion` VARCHAR(255), `libraryFiles` JSON, `extraData` JSON, `title` VARCHAR(255), `titleIgnorePrefix` VARCHAR(255), `authorNamesFirstLast` VARCHAR(255), `authorNamesLastFirst` VARCHAR(255), `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `libraryId` UUID REFERENCES `libraries` (`id`) ON DELETE SET NULL ON UPDATE CASCADE, `libraryFolderId` UUID REFERENCES `libraryFolders` (`id`) ON DELETE SET NULL ON UPDATE CASCADE);

CREATE TABLE `mediaItemShares` (`id` UUID PRIMARY KEY, `mediaItemId` UUID, `mediaItemType` VARCHAR(255), `slug` VARCHAR(255), `pash` VARCHAR(255), `expiresAt` DATETIME, `extraData` JSON, `isDownloadable` TINYINT(1), `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `userId` UUID REFERENCES `users` (`id`) ON DELETE SET NULL ON UPDATE CASCADE);

CREATE TABLE `mediaProgresses` (`id` UUID PRIMARY KEY, `mediaItemId` UUID, `mediaItemType` VARCHAR(255), `duration` FLOAT, `currentTime` FLOAT, `isFinished` TINYINT(1), `hideFromContinueListening` TINYINT(1), `ebookLocation` VARCHAR(255), `ebookProgress` FLOAT, `finishedAt` DATETIME, `extraData` JSON, `podcastId` UUID, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `userId` UUID REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `migrationsMeta` (`key` VARCHAR(255) NOT NULL, `value` VARCHAR(255) NOT NULL);

CREATE TABLE `playbackSessions` (`id` UUID PRIMARY KEY, `mediaItemId` UUID, `mediaItemType` VARCHAR(255), `displayTitle` VARCHAR(255), `displayAuthor` VARCHAR(255), `duration` FLOAT, `playMethod` INTEGER, `mediaPlayer` VARCHAR(255), `startTime` FLOAT, `currentTime` FLOAT, `serverVersion` VARCHAR(255), `coverPath` VARCHAR(255), `timeListening` INTEGER, `mediaMetadata` JSON, `date` VARCHAR(255), `dayOfWeek` VARCHAR(255), `extraData` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `userId` UUID REFERENCES `users` (`id`) ON DELETE SET NULL ON UPDATE CASCADE, `deviceId` UUID REFERENCES `devices` (`id`) ON DELETE SET NULL ON UPDATE CASCADE, `libraryId` UUID REFERENCES `libraries` (`id`) ON DELETE SET NULL ON UPDATE CASCADE);

CREATE TABLE `playlistMediaItems` (`id` UUID PRIMARY KEY, `mediaItemId` UUID, `mediaItemType` VARCHAR(255), `order` INTEGER, `createdAt` DATETIME NOT NULL, `playlistId` UUID REFERENCES `playlists` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `playlists` (`id` UUID PRIMARY KEY, `name` VARCHAR(255), `description` TEXT, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `libraryId` UUID REFERENCES `libraries` (`id`) ON DELETE SET NULL ON UPDATE CASCADE, `userId` UUID REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `podcastEpisodes` (`id` UUID PRIMARY KEY, `index` INTEGER, `season` VARCHAR(255), `episode` VARCHAR(255), `episodeType` VARCHAR(255), `title` VARCHAR(255), `subtitle` VARCHAR(1000), `description` TEXT, `pubDate` VARCHAR(255), `enclosureURL` VARCHAR(255), `enclosureSize` BIGINT, `enclosureType` VARCHAR(255), `publishedAt` DATETIME, `audioFile` JSON, `chapters` JSON, `extraData` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `podcastId` UUID REFERENCES `podcasts` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `podcasts` (`id` UUID PRIMARY KEY, `title` VARCHAR(255), `titleIgnorePrefix` VARCHAR(255), `author` VARCHAR(255), `releaseDate` VARCHAR(255), `feedURL` VARCHAR(255), `imageURL` VARCHAR(255), `description` TEXT, `itunesPageURL` VARCHAR(255), `itunesId` VARCHAR(255), `itunesArtistId` VARCHAR(255), `language` VARCHAR(255), `podcastType` VARCHAR(255), `explicit` TINYINT(1), `autoDownloadEpisodes` TINYINT(1), `autoDownloadSchedule` VARCHAR(255), `lastEpisodeCheck` DATETIME, `maxEpisodesToKeep` INTEGER, `maxNewEpisodesToDownload` INTEGER, `coverPath` VARCHAR(255), `tags` JSON, `genres` JSON, `numEpisodes` INTEGER, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL);

CREATE TABLE `series` (`id` UUID PRIMARY KEY, `name` VARCHAR(255), `nameIgnorePrefix` VARCHAR(255), `description` TEXT, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `libraryId` UUID REFERENCES `libraries` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `sessions` (`id` UUID PRIMARY KEY, `ipAddress` VARCHAR(255), `userAgent` VARCHAR(255), `refreshToken` VARCHAR(255) NOT NULL, `expiresAt` DATETIME NOT NULL, `lastRefreshToken` VARCHAR(255), `lastRefreshTokenExpiresAt` DATETIME, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL, `userId` UUID NOT NULL REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE CASCADE);

CREATE TABLE `settings` (`key` VARCHAR(255) PRIMARY KEY, `value` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL);

CREATE TABLE `users` (`id` UUID PRIMARY KEY, `username` VARCHAR(255), `email` VARCHAR(255), `pash` VARCHAR(255), `type` VARCHAR(255), `token` VARCHAR(255), `isActive` TINYINT(1) DEFAULT 0, `isLocked` TINYINT(1) DEFAULT 0, `lastSeen` DATETIME, `permissions` JSON, `bookmarks` JSON, `extraData` JSON, `createdAt` DATETIME NOT NULL, `updatedAt` DATETIME NOT NULL);

CREATE INDEX `authors_library_id` ON `authors` (`libraryId`);

CREATE INDEX `authors_name` ON `authors` (`name` COLLATE `NOCASE`);

CREATE INDEX `bookAuthor_authorId` ON `bookAuthors` (`authorId`);

CREATE INDEX `bookSeries_seriesId` ON `bookSeries` (`seriesId`);

CREATE INDEX `book_series_series_book` ON `bookSeries` (`seriesId`, `bookId`);

CREATE INDEX `books_duration` ON `books` (`duration`);

CREATE INDEX `books_published_year` ON `books` (`publishedYear`);

CREATE INDEX `books_title` ON `books` (`title` COLLATE `NOCASE`);

CREATE INDEX `library_items_birthtime` ON `libraryItems` (`birthtime`);

CREATE INDEX `library_items_created_at` ON `libraryItems` (`createdAt`);

CREATE INDEX `library_items_library_id_media_id_media_type` ON `libraryItems` (`libraryId`, `mediaId`, `mediaType`);

CREATE INDEX `library_items_library_id_media_type` ON `libraryItems` (`libraryId`, `mediaType`);

CREATE INDEX `library_items_library_id_media_type_author_names_first_last` ON `libraryItems` (`libraryId`, `mediaType`, `authorNamesFirstLast` COLLATE `NOCASE`);

CREATE INDEX `library_items_library_id_media_type_author_names_last_first` ON `libraryItems` (`libraryId`, `mediaType`, `authorNamesLastFirst` COLLATE `NOCASE`);

CREATE INDEX `library_items_library_id_media_type_created_at` ON `libraryItems` (`libraryId`, `mediaType`, `createdAt`);

CREATE INDEX `library_items_library_id_media_type_size` ON `libraryItems` (`libraryId`, `mediaType`, `size`);

CREATE INDEX `library_items_library_id_media_type_title` ON `libraryItems` (`libraryId`, `mediaType`, `title` COLLATE `NOCASE`);

CREATE INDEX `library_items_library_id_media_type_title_ignore_prefix` ON `libraryItems` (`libraryId`, `mediaType`, `titleIgnorePrefix` COLLATE `NOCASE`);

CREATE INDEX `library_items_media_id` ON `libraryItems` (`mediaId`);

CREATE INDEX `library_items_mtime` ON `libraryItems` (`mtime`);

CREATE INDEX `media_progresses_updated_at` ON `mediaProgresses` (`updatedAt`);

CREATE INDEX `media_progresses_user_item_finished_time` ON `mediaProgresses` (`userId`, `mediaItemId`, `isFinished`, `currentTime`);

CREATE INDEX `podcastEpisode_createdAt_podcastId` ON `podcastEpisodes` (`createdAt`, `podcastId`);

CREATE INDEX `podcast_episodes_published_at` ON `podcastEpisodes` (`publishedAt`);

CREATE INDEX `series_library_id` ON `series` (`libraryId`);

CREATE INDEX `series_name` ON `series` (`name` COLLATE `NOCASE`);

CREATE UNIQUE INDEX `unique_series_name_per_library` ON `series` (`name`, `libraryId`);

CREATE TRIGGER update_library_items_author_names_on_authors_update
          AFTER UPDATE OF name ON authors
          FOR EACH ROW
          BEGIN
            UPDATE libraryItems
              SET (authorNamesFirstLast, authorNamesLastFirst) = (
        SELECT GROUP_CONCAT(authors.name, ', ' ORDER BY bookAuthors.createdAt ASC), GROUP_CONCAT(authors.lastFirst, ', ' ORDER BY bookAuthors.createdAt ASC)
        FROM authors JOIN bookAuthors ON authors.id = bookAuthors.authorId
        WHERE bookAuthors.bookId = libraryItems.mediaId
      )
            WHERE mediaId IN (SELECT bookId FROM bookAuthors WHERE authorId = NEW.id);
        END;

CREATE TRIGGER update_library_items_author_names_on_book_authors_delete
          AFTER delete ON bookAuthors
          FOR EACH ROW
          BEGIN
            UPDATE libraryItems
              SET (authorNamesFirstLast, authorNamesLastFirst) = (
        SELECT GROUP_CONCAT(authors.name, ', ' ORDER BY bookAuthors.createdAt ASC), GROUP_CONCAT(authors.lastFirst, ', ' ORDER BY bookAuthors.createdAt ASC)
        FROM authors JOIN bookAuthors ON authors.id = bookAuthors.authorId
        WHERE bookAuthors.bookId = OLD.bookId
      )
            WHERE mediaId = OLD.bookId;
          END;

CREATE TRIGGER update_library_items_author_names_on_book_authors_insert
          AFTER insert ON bookAuthors
          FOR EACH ROW
          BEGIN
            UPDATE libraryItems
              SET (authorNamesFirstLast, authorNamesLastFirst) = (
        SELECT GROUP_CONCAT(authors.name, ', ' ORDER BY bookAuthors.createdAt ASC), GROUP_CONCAT(authors.lastFirst, ', ' ORDER BY bookAuthors.createdAt ASC)
        FROM authors JOIN bookAuthors ON authors.id = bookAuthors.authorId
        WHERE bookAuthors.bookId = NEW.bookId
      )
            WHERE mediaId = NEW.bookId;
          END;

CREATE TRIGGER update_library_items_title
        AFTER UPDATE OF title ON books
        FOR EACH ROW
        BEGIN
          UPDATE libraryItems
            SET title = NEW.title
          WHERE libraryItems.mediaId = NEW.id;
        END;

CREATE TRIGGER update_library_items_title_from_podcasts_title
        AFTER UPDATE OF title ON podcasts
        FOR EACH ROW
        BEGIN
          UPDATE libraryItems
            SET title = NEW.title
          WHERE libraryItems.mediaId = NEW.id;
        END;

CREATE TRIGGER update_library_items_title_ignore_prefix
        AFTER UPDATE OF titleIgnorePrefix ON books
        FOR EACH ROW
        BEGIN
          UPDATE libraryItems
            SET titleIgnorePrefix = NEW.titleIgnorePrefix
          WHERE libraryItems.mediaId = NEW.id;
        END;

CREATE TRIGGER update_library_items_title_ignore_prefix_from_podcasts_title_ignore_prefix
        AFTER UPDATE OF titleIgnorePrefix ON podcasts
        FOR EACH ROW
        BEGIN
          UPDATE libraryItems
            SET titleIgnorePrefix = NEW.titleIgnorePrefix
          WHERE libraryItems.mediaId = NEW.id;
        END;
