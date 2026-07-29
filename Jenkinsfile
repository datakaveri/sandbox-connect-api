def SERVICES = [
    api: [
        image       : 'mahaagx-sandbox-connect-api',
        dockerfile  : 'infra/api/Dockerfile',
        manifestPath: '~/sandbox-connect-api/infra/api/manifest.yaml',
        k8sNamespace: 'sandbox',
        k8sKind     : 'deployment',
        k8sName     : 'sandbox-api',
    ],
    worker: [
        image       : 'tgdex-sandbox-connect-worker',
        dockerfile  : 'infra/worker/Dockerfile',
        manifestPath: '~/sandbox-connect-api/infra/worker/deployment.yaml',
        k8sNamespace: 'sandbox',
        k8sKind     : 'deployment',
        k8sName     : 'sandbox-worker',
    ],
    platformTokenSidecar: [
        image       : 'platform-token-sidecar',
        dockerfile  : 'infra/platform-token-sidecar/Dockerfile',
        // Injected by the worker into notebook sidecar pods via infra/worker/configmap.yaml
        // at spawn time -- there's no standalone Deployment to roll here.
        manifestPath: null,
    ],
    slotLifecycle: [
        image       : 'sandbox-connect-slot-lifecycle',
        dockerfile  : 'infra/cron/slot-lifecycle/Dockerfile',
        manifestPath: '~/sandbox-connect-api/infra/cron/slot-lifecycle/deployment.yaml',
        k8sNamespace: 'sandbox',
        k8sKind     : 'deployment',
        k8sName     : 'slot-lifecycle',
    ],
    profileCreditSync: [
        image       : 'mahaagx-sandbox-credit-sync-cron',
        dockerfile  : 'infra/cron/profile-credit-sync/Dockerfile',
        manifestPath: '~/sandbox-connect-api/infra/cron/profile-credit-sync/cronjob.yaml',
        k8sNamespace: 'sandbox',
        k8sKind     : 'cronjob',
        k8sName     : 'profile-credit-sync',
    ],
]

def ensureGhcrRepoExists(String image) {
    withCredentials([usernamePassword(credentialsId: registryCredential, usernameVariable: 'GHCR_USER', passwordVariable: 'GHCR_TOKEN')]) {
        def httpStatus = sh(
            script: """
                curl -s -o /dev/null -w '%{http_code}' \
                  -H "Authorization: Bearer \$GHCR_TOKEN" \
                  -H "Accept: application/vnd.github+json" \
                  https://api.github.com/orgs/${GHCR_ORG}/packages/container/${image}
            """,
            returnStdout: true
        ).trim()

        if (httpStatus == '404') {
            echo "GHCR package '${image}' does not exist yet — it will be created automatically on the first docker push."
        } else if (httpStatus == '200') {
            echo "GHCR package '${image}' already exists."
        } else {
            echo "Could not verify GHCR package '${image}' (HTTP ${httpStatus}); continuing — docker push will create it if missing."
        }
    }
}

def deployService(String key, Map svc, String imageRef) {
    if (!svc.manifestPath) {
        echo "No cluster deploy target configured for ${key}; skipping CD."
        return
    }

    // PR builds are validated (build + scan + push) but not rolled onto the shared dev cluster.
    if (!(env.GIT_BRANCH == 'origin/dev')) {
        echo "Branch ${env.GIT_BRANCH} is not dev; skipping CD for ${key}."
        return
    }

    def rolloutCheck = (svc.k8sKind == 'deployment')
        ? " && kubectl rollout status deployment/${svc.k8sName} -n ${svc.k8sNamespace} --timeout=5m"
        : ''

    try {
        sh """
          ssh ubuntu@dev-eks 'sed -i "s|image: .*|image: ${imageRef}|g" ${svc.manifestPath} && kubectl apply -f ${svc.manifestPath} -n ${svc.k8sNamespace}${rolloutCheck}'
        """
    } catch (Exception e) {
        error "Failed to deploy ${key} to EKS"
    }
}

def processService(String key, Map svc) {
    def image = "${GHCR_REGISTRY}/${GHCR_ORG}/${svc.image}"
    def imageRef = "${image}:${env.SHA7}"

    echo "Building ${key} from ${svc.dockerfile}"
    def builtImage = docker.build(imageRef, "-f ${svc.dockerfile} .")

    sh "trivy image --output trivy-image-${key}-report.txt ${imageRef}"
    archiveArtifacts artifacts: "trivy-image-${key}-report.txt", allowEmptyArchive: true

    try {
        sh """
          trivy image \
            --exit-code 1 \
            --severity HIGH,CRITICAL \
            --ignore-unfixed \
            ${imageRef}
        """
    } catch (Exception e) {
        echo "Trivy scan failed for ${key} due to high or critical vulnerabilities."
        throw e
    }

    ensureGhcrRepoExists(svc.image)
    docker.withRegistry(registryUri, registryCredential) {
        builtImage.push()
    }

    deployService(key, svc, imageRef)
}

pipeline {
    agent {
        node {
            label 'slave1'
        }
    }

    parameters {
        booleanParam(name: 'FORCE_BUILD_ALL', defaultValue: false, description: 'Build, scan, push and (on dev only) deploy all 5 services regardless of which paths changed')
    }

    environment {
        GHCR_REGISTRY      = 'ghcr.io'
        GHCR_ORG           = 'datakaveri'
        registryUri        = 'https://ghcr.io'
        registryCredential = 'datakaveri-ghcr'
    }

    options {
        disableConcurrentBuilds()
        timestamps()
    }

    stages {
        stage('Prep') {
            steps {
                script {
                    env.SHA7 = sh(script: 'git rev-parse --short=7 HEAD', returnStdout: true).trim()
                }
            }
        }

        stage('Conditional Execution') {
            when {
                expression {
                    return env.GIT_BRANCH == 'origin/dev' || env.GIT_BRANCH?.startsWith('origin/PR-')
                }
            }

            stages {
                stage('Trivy Code Scan (Dependencies)') {
                    steps {
                        sh 'trivy fs --scanners vuln,secret,misconfig --output trivy-fs-report.txt .'
                    }
                    post {
                        always {
                            archiveArtifacts artifacts: 'trivy-fs-report.txt', allowEmptyArchive: true
                        }
                    }
                }

                stage('Build, Scan, Push & Deploy') {
                    parallel {
                        stage('api') {
                            when {
                                anyOf {
                                    changeset 'cmd/api/**'
                                    changeset 'pkg/**'
                                    changeset 'docs/**'
                                    changeset 'jupyterlite-content/**'
                                    changeset 'infra/api/Dockerfile'
                                    changeset 'go.mod'
                                    changeset 'go.sum'
                                    expression { params.FORCE_BUILD_ALL }
                                }
                            }
                            steps { script { processService('api', SERVICES.api) } }
                        }

                        stage('worker') {
                            when {
                                anyOf {
                                    changeset 'cmd/worker/**'
                                    changeset 'pkg/**'
                                    changeset 'infra/worker/Dockerfile'
                                    changeset 'go.mod'
                                    changeset 'go.sum'
                                    expression { params.FORCE_BUILD_ALL }
                                }
                            }
                            steps { script { processService('worker', SERVICES.worker) } }
                        }

                        stage('platform-token-sidecar') {
                            when {
                                anyOf {
                                    changeset 'cmd/platform-token-sidecar/**'
                                    changeset 'pkg/**'
                                    changeset 'infra/platform-token-sidecar/Dockerfile'
                                    changeset 'go.mod'
                                    changeset 'go.sum'
                                    expression { params.FORCE_BUILD_ALL }
                                }
                            }
                            steps { script { processService('platform-token-sidecar', SERVICES.platformTokenSidecar) } }
                        }

                        stage('slot-lifecycle') {
                            when {
                                anyOf {
                                    changeset 'cmd/cron/slot-lifecycle/**'
                                    changeset 'pkg/**'
                                    changeset 'infra/cron/slot-lifecycle/Dockerfile'
                                    changeset 'go.mod'
                                    changeset 'go.sum'
                                    expression { params.FORCE_BUILD_ALL }
                                }
                            }
                            steps { script { processService('slot-lifecycle', SERVICES.slotLifecycle) } }
                        }

                        stage('profile-credit-sync') {
                            when {
                                anyOf {
                                    changeset 'cmd/cron/profile-credit-sync/**'
                                    changeset 'pkg/**'
                                    changeset 'infra/cron/profile-credit-sync/Dockerfile'
                                    changeset 'go.mod'
                                    changeset 'go.sum'
                                    expression { params.FORCE_BUILD_ALL }
                                }
                            }
                            steps { script { processService('profile-credit-sync', SERVICES.profileCreditSync) } }
                        }
                    }
                }
            }
        }
    }

    post {
        failure {
            script {
                if (env.GIT_BRANCH == 'origin/dev') {
                    emailext(
                        recipientProviders: [buildUser(), developers()],
                        to: '$AAA_RECIPIENTS, $DEFAULT_RECIPIENTS',
                        subject: '$PROJECT_NAME - Build # $BUILD_NUMBER - $BUILD_STATUS!',
                        body: '''$PROJECT_NAME - Build # $BUILD_NUMBER - $BUILD_STATUS:
Check console output at $BUILD_URL to view the results.'''
                    )
                }
            }
        }
    }
}
