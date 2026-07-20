import java.util.Properties

plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
}

android {
    namespace = "com.zzlye.poolwatch"
    compileSdk = 35

    defaultConfig {
        applicationId = "com.zzlye.poolwatch"
        minSdk = 26
        targetSdk = 35
        versionCode = 2
        versionName = "1.1.0"

        testInstrumentationRunner = "androidx.test.runner.AndroidJUnitRunner"
        vectorDrawables.useSupportLibrary = true
        buildConfigField("String", "DEFAULT_SERVER_URL", "\"https://jiance.zzlye.xyz\"")
    }

    val signingProperties = Properties().apply {
        val propertiesFile = file("keystore.properties")
        if (propertiesFile.isFile) propertiesFile.inputStream().use(::load)
    }
    val releaseKeystorePath = System.getenv("POOLWATCH_KEYSTORE_PATH")
        ?.takeIf(String::isNotBlank)
        ?: signingProperties.getProperty("storeFile")?.takeIf(String::isNotBlank)
    val hasReleaseKeystore = !releaseKeystorePath.isNullOrBlank()
    signingConfigs {
        if (hasReleaseKeystore) {
            create("release") {
                storeFile = file(requireNotNull(releaseKeystorePath))
                storePassword = System.getenv("POOLWATCH_KEYSTORE_PASSWORD")
                    ?: signingProperties.getProperty("storePassword")
                keyAlias = System.getenv("POOLWATCH_KEY_ALIAS")
                    ?: signingProperties.getProperty("keyAlias")
                keyPassword = System.getenv("POOLWATCH_KEY_PASSWORD")
                    ?: signingProperties.getProperty("keyPassword")
            }
        }
    }

    buildTypes {
        debug {
            applicationIdSuffix = ".debug"
            versionNameSuffix = "-debug"
        }
        release {
            isMinifyEnabled = true
            isShrinkResources = true
            signingConfig = if (hasReleaseKeystore) {
                signingConfigs.getByName("release")
            } else {
                null
            }
            proguardFiles(
                getDefaultProguardFile("proguard-android-optimize.txt"),
                "proguard-rules.pro",
            )
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
        buildConfig = true
    }

    packaging {
        resources.excludes += "/META-INF/{AL2.0,LGPL2.1}"
    }

    lint {
        // 当前依赖组合已经针对目标系统完成验证，版本更新提示不阻断构建。
        disable += "GradleDependency"
    }
}

dependencies {
    val composeBom = platform("androidx.compose:compose-bom:2025.02.00")

    implementation(composeBom)
    androidTestImplementation(composeBom)

    implementation("androidx.core:core-ktx:1.15.0")
    implementation("androidx.activity:activity-compose:1.10.1")
    implementation("androidx.lifecycle:lifecycle-runtime-ktx:2.8.7")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.ui:ui-tooling-preview")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-extended")
    implementation("androidx.work:work-runtime-ktx:2.10.0")
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("com.squareup.okhttp3:okhttp-sse:4.12.0")

    debugImplementation("androidx.compose.ui:ui-tooling")
    debugImplementation("androidx.compose.ui:ui-test-manifest")

    testImplementation("junit:junit:4.13.2")
    // 本地单元测试使用真实 JSON 实现，避免 Android 桩类在 JVM 中抛出异常。
    testImplementation("org.json:json:20240303")
    androidTestImplementation("androidx.test.ext:junit:1.2.1")
    androidTestImplementation("androidx.test.espresso:espresso-core:3.6.1")
    androidTestImplementation("androidx.compose.ui:ui-test-junit4")
}
