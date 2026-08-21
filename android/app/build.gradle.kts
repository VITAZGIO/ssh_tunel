plugins {
	id("com.android.application")
	id("org.jetbrains.kotlin.android")
}

android {
	namespace = "io.github.vitazgio.sshtunnel"
	compileSdk = 35

	defaultConfig {
		applicationId = "io.github.vitazgio.sshtunnel"
		// Android 8: ниже нет ни excludeRoute, ни половины нужного в VpnService.
		minSdk = 26
		targetSdk = 35
		// Версию задаёт сборка релиза, чтобы не помнить про правку этого файла
		// при каждом выпуске. Значения ниже — запасные, для сборки руками.
		//
		// versionCode обязан расти от версии к версии: по нему Android решает,
		// обновление это или откат. Имя показывается человеку и ни на что не
		// влияет.
		versionCode = System.getenv("ANDROID_VERSION_CODE")?.toIntOrNull() ?: 2
		versionName = (System.getenv("ANDROID_VERSION_NAME") ?: "1.1.0").removePrefix("v")
	}

	// Подпись выпускаемой сборки.
	//
	// Android опознаёт приложение по ключу, которым оно подписано. Обновление
	// он ставит поверх только тогда, когда ключ тот же самый; подписанное
	// другим ключом считается посторонним приложением, и поставить его можно
	// лишь удалив прежнее — вместе с настройками. Поэтому ключ обязан быть
	// постоянным и жить не в репозитории, а в секретах сборки.
	//
	// Значения приходят из переменных окружения. Их нет (сборка на своей
	// машине или чужой форк) — конфигурации подписи не будет, и release-сборка
	// просто не соберётся; для проверок есть debug.
	val storeFilePath = System.getenv("ANDROID_KEYSTORE_FILE")
	val storePass = System.getenv("ANDROID_KEYSTORE_PASSWORD")
	val keyName = System.getenv("ANDROID_KEY_ALIAS")
	val keyPass = System.getenv("ANDROID_KEY_PASSWORD")
	val canSign = !storeFilePath.isNullOrBlank() && file(storeFilePath).exists()

	signingConfigs {
		if (canSign) {
			create("release") {
				storeFile = file(storeFilePath!!)
				storePassword = storePass
				keyAlias = keyName
				keyPassword = keyPass
			}
		}
	}

	buildTypes {
		release {
			isMinifyEnabled = false
			if (canSign) {
				signingConfig = signingConfigs.getByName("release")
			}
		}
	}

	compileOptions {
		sourceCompatibility = JavaVersion.VERSION_17
		targetCompatibility = JavaVersion.VERSION_17
	}
	kotlin {
		compilerOptions {
			jvmTarget = org.jetbrains.kotlin.gradle.dsl.JvmTarget.JVM_17
		}
	}

	sourceSets["main"].java.srcDir("src/main/kotlin")
}

dependencies {
	// core.aar собирается из ../core перед сборкой приложения.
	implementation(fileTree("libs") { include("*.aar") })
	implementation("androidx.appcompat:appcompat:1.7.0")
	implementation("com.google.android.material:material:1.12.0")
}
