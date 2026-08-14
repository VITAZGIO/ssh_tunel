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
		versionCode = 1
		versionName = "0.1.0"
	}

	buildTypes {
		release {
			isMinifyEnabled = false
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
